package coordination

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const MaxCommandBytes = 128 << 10
const MaxPlanBytes = 8 << 20

type Store struct {
	db  *sql.DB
	now func() time.Time
}

const schema = `
PRAGMA application_id = 1280790354;
CREATE TABLE metadata (version INTEGER NOT NULL CHECK(version=1));
INSERT INTO metadata VALUES (1);
CREATE TABLE members (id TEXT PRIMARY KEY, permissions TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL);
CREATE TABLE plans (id TEXT PRIMARY KEY, body TEXT NOT NULL);
CREATE TABLE events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL, request_id TEXT NOT NULL,
 digest TEXT NOT NULL, command TEXT NOT NULL, receipt TEXT NOT NULL, recorded_at TEXT NOT NULL,
 UNIQUE(actor,request_id)
);
CREATE TABLE membership_events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, member TEXT NOT NULL,
 permissions TEXT NOT NULL, recorded_at TEXT NOT NULL);
`

// Open owns only an orchestration database. Creation is explicit, and the parent
// must be private so SQLite journals cannot expose team context or token hashes.
func Open(path string, create bool) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if create {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database directory must be private (mode 0700)")
	}
	if create {
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a private regular file (mode 0600)")
	}
	u := url.URL{Scheme: "file", Path: path}
	if !create {
		probe, e := sql.Open("sqlite", u.String()+"?mode=ro")
		if e != nil {
			return nil, e
		}
		var applicationID, version int
		e = probe.QueryRow("PRAGMA application_id").Scan(&applicationID)
		if e == nil && applicationID == 1280790354 {
			e = probe.QueryRow("SELECT version FROM metadata").Scan(&version)
		}
		probe.Close()
		if e != nil || applicationID != 1280790354 || version != SchemaVersion {
			return nil, errors.New("not a supported orchestration database; refusing to open for writing")
		}
	}
	db, err := sql.Open("sqlite", u.String()+"?mode=rw&_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	if create {
		if _, err = db.Exec(schema); err != nil {
			return fail(err)
		}
	}
	var version int
	if err = db.QueryRow("SELECT version FROM metadata").Scan(&version); err != nil {
		return fail(errors.New("not an initialized orchestration database"))
	}
	if version != SchemaVersion {
		return fail(errors.New("unsupported orchestration database version"))
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func randomID() string { return hex.EncodeToString(randomBytes(24)) }
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Enroll is an operator-only local operation, never exposed by the HTTP API.
// Re-enrollment rotates the credential. An empty permission list is read-only.
func (s *Store) Enroll(ctx context.Context, id string, permissions []string) (string, error) {
	if !validID(id) || cleanString(id) != id {
		return "", errors.New("invalid member identifier")
	}
	for _, p := range permissions {
		if p != "plan" && p != "accept" && p != "assign" && p != "review" && p != "work" {
			return "", errors.New("unknown member permission")
		}
	}
	encoded, _ := json.Marshal(permissions)
	token := "lworch_" + hex.EncodeToString(randomBytes(32))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO members VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET permissions=excluded.permissions,token_hash=excluded.token_hash", id, string(encoded), digest([]byte(token)))
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO membership_events(member,permissions,recorded_at) VALUES(?,?,?)", id, string(encoded), s.now().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) Revoke(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM members WHERE id=?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO membership_events(member,permissions,recorded_at) VALUES(?,?,?)", id, "revoked", s.now().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authenticate(ctx context.Context, q queryer, token string) (Member, error) {
	var m Member
	var permissions string
	if len(token) != 71 || !strings.HasPrefix(token, "lworch_") {
		return m, ErrForbidden
	}
	err := q.QueryRowContext(ctx, "SELECT id,permissions FROM members WHERE token_hash=?", digest([]byte(token))).Scan(&m.ID, &permissions)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrForbidden
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal([]byte(permissions), &m.Permissions)
	return m, err
}

func loadPlan(ctx context.Context, q queryer, id string) (*Plan, error) {
	var body []byte
	if err := q.QueryRowContext(ctx, "SELECT body FROM plans WHERE id=?", id).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) Execute(ctx context.Context, token string, c Command) (Receipt, error) {
	var empty Receipt
	raw, err := json.Marshal(c)
	if err != nil {
		return empty, err
	}
	if len(raw) > MaxCommandBytes {
		return empty, errors.New("command too large")
	}
	requestDigest := digest(raw)
	if c.SchemaVersion != SchemaVersion || !validID(c.RequestID) || !validID(c.PlanID) {
		return empty, errors.New("schema_version 1 and valid request_id/plan_id required")
	}
	c, err = cleanCommand(c)
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	m, err := authenticate(ctx, tx, token)
	if err != nil {
		return empty, err
	}
	var previousDigest string
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT digest,receipt FROM events WHERE actor=? AND request_id=?", m.ID, c.RequestID).Scan(&previousDigest, &previous)
	if err == nil {
		if previousDigest != requestDigest {
			return empty, errors.New("request_id already used for different input")
		}
		var r Receipt
		err = json.Unmarshal(previous, &r)
		return r, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	p, err := loadPlan(ctx, tx, c.PlanID)
	if errors.Is(err, ErrNotFound) && c.Action == "propose" {
		p = &Plan{ID: c.PlanID, Work: map[string]*Work{}, Attempts: map[string]*Attempt{}}
		err = nil
	}
	if err != nil {
		return empty, err
	}
	if c.Action == "assign" && c.Engineer != "" {
		var perms string
		if err = tx.QueryRowContext(ctx, "SELECT permissions FROM members WHERE id=?", c.Engineer).Scan(&perms); errors.Is(err, sql.ErrNoRows) {
			return empty, errors.New("assignee is not an enrolled engineer")
		} else if err != nil {
			return empty, err
		}
		var target Member
		json.Unmarshal([]byte(perms), &target.Permissions)
		if !target.Can("work") {
			return empty, errors.New("assignee lacks work permission")
		}
	}
	now := s.now()
	r, err := apply(p, m, c, now, randomID())
	if err != nil {
		return empty, err
	}
	body, err := json.Marshal(p)
	if err != nil {
		return empty, err
	}
	if len(body) > MaxPlanBytes {
		return empty, errors.New("plan exceeds the 8 MiB pilot limit; keep artifacts external and split future work into another plan")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO plans VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", p.ID, string(body)); err != nil {
		return empty, err
	}
	command, _ := json.Marshal(c)
	receipt, _ := json.Marshal(r)
	if _, err = tx.ExecContext(ctx, "INSERT INTO events(actor,request_id,digest,command,receipt,recorded_at) VALUES(?,?,?,?,?,?)", m.ID, c.RequestID, requestDigest, string(command), string(receipt), now.Format(time.RFC3339Nano)); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return r, nil
}

func (s *Store) Read(ctx context.Context, token, id string) (View, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return View{}, err
	}
	defer tx.Rollback()
	m, err := authenticate(ctx, tx, token)
	if err != nil {
		return View{}, err
	}
	p, err := loadPlan(ctx, tx, id)
	if err != nil {
		return View{}, err
	}
	return p.view(m, s.now()), nil
}

type Summary struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Version          int    `json:"version"`
	AcceptedRevision int    `json:"accepted_revision"`
}

func (s *Store) List(ctx context.Context, token string) ([]Summary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, token); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM plans ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Summary{}
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var p Plan
		if err = json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		list = append(list, Summary{ID: p.ID, Title: p.Revisions[len(p.Revisions)-1].Definition.Title, Version: p.Version, AcceptedRevision: p.AcceptedRevision})
	}
	return list, rows.Err()
}

// History pages the redacted, authenticated mutation ledger. Request hashes and
// credential records remain private to the coordinator.
func (s *Store) History(ctx context.Context, token, id string, after int64) (History, error) {
	h := History{SchemaVersion: SchemaVersion, Events: []Event{}, NextAfter: after}
	if after < 0 {
		return h, errors.New("after must be nonnegative")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return h, err
	}
	defer tx.Rollback()
	if _, err = authenticate(ctx, tx, token); err != nil {
		return h, err
	}
	if _, err = loadPlan(ctx, tx, id); err != nil {
		return h, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT sequence,actor,command,receipt,recorded_at FROM events WHERE json_extract(command,'$.plan_id')=? AND sequence>? ORDER BY sequence LIMIT 100", id, after)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var command, receipt []byte
		if err = rows.Scan(&e.Sequence, &e.Actor, &command, &receipt, &e.RecordedAt); err != nil {
			return h, err
		}
		if err = json.Unmarshal(command, &e.Command); err != nil {
			return h, err
		}
		if err = json.Unmarshal(receipt, &e.Receipt); err != nil {
			return h, err
		}
		h.Events = append(h.Events, e)
		h.NextAfter = e.Sequence
	}
	return h, rows.Err()
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|token|password|secret)["']?\s*[=:]\s*["'])[^"'\r\n]+`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s"']+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[=:]\s*)[^\s"']+`),
	regexp.MustCompile(`(?i)(https?://[^:/\s]+:)[^@\s]+(@)`),
}
var rawSecret = regexp.MustCompile(`lworch_[a-f0-9]{64}|\bsk-[a-zA-Z0-9_-]{16,}|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)

func cleanString(s string) string {
	for _, pattern := range secretPatterns {
		s = pattern.ReplaceAllString(s, `${1}[REDACTED]${2}`)
	}
	return rawSecret.ReplaceAllString(s, "[REDACTED]")
}

func cleanCommand(c Command) (Command, error) {
	for _, id := range []string{c.RequestID, c.PlanID, c.ItemID, c.Engineer, c.Machine, c.Agent, c.AttemptID, c.SubmissionID} {
		if id != "" && (!validID(id) || cleanString(id) != id) {
			return c, errors.New("invalid or sensitive identifier")
		}
	}
	// Decode structurally: redact individual string values before re-encoding,
	// so JSON escaping cannot conceal credentials. No arbitrary env/stdin fields.
	body, _ := json.Marshal(c)
	var v any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&v); err != nil {
		return c, err
	}
	var walk func(any) (any, error)
	walk = func(value any) (any, error) {
		switch x := value.(type) {
		case string:
			if len(x) > 16384 {
				return nil, errors.New("text field exceeds 16 KiB")
			}
			return cleanString(x), nil
		case []any:
			for i, a := range x {
				b, e := walk(a)
				if e != nil {
					return nil, e
				}
				x[i] = b
			}
		case map[string]any:
			for k, a := range x {
				if strings.HasSuffix(k, "_refs") {
					if refs, ok := a.([]any); ok {
						for _, ref := range refs {
							if value, ok := ref.(string); !ok || strings.TrimSpace(value) == "" {
								return nil, errors.New("references must not be blank")
							}
						}
					}
				}
				b, e := walk(a)
				if e != nil {
					return nil, e
				}
				x[k] = b
			}
		}
		return value, nil
	}
	v, err := walk(v)
	if err != nil {
		return c, err
	}
	body, _ = json.Marshal(v)
	var sanitized Command
	err = json.Unmarshal(body, &sanitized)
	if err != nil {
		return c, fmt.Errorf("sanitize command: %w", err)
	}
	return sanitized, nil
}
