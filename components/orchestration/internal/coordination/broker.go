package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// A local broker has a distinct database identity. It cannot open a team store
// or grant work claims. Database-local sequence numbers are never portable IDs.
const brokerSchema = `
PRAGMA application_id = 1280790355;
CREATE TABLE metadata (version INTEGER NOT NULL CHECK(version=1));
INSERT INTO metadata VALUES (1);
CREATE TABLE members (id TEXT PRIMARY KEY, permissions TEXT NOT NULL, token_hash TEXT UNIQUE NOT NULL);
CREATE TABLE membership_events (sequence INTEGER PRIMARY KEY AUTOINCREMENT, member TEXT NOT NULL, permissions TEXT NOT NULL, recorded_at TEXT NOT NULL);
CREATE TABLE broker (id TEXT NOT NULL, project TEXT NOT NULL, machine TEXT NOT NULL);
CREATE TABLE sessions (id TEXT PRIMARY KEY, actor TEXT NOT NULL, body TEXT NOT NULL);
CREATE INDEX session_recency ON sessions(json_extract(body,'$.updated_at') DESC,id);
CREATE TABLE messages (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL,
 actor TEXT NOT NULL, request_id TEXT NOT NULL, digest TEXT NOT NULL, body TEXT NOT NULL,
 receipt TEXT NOT NULL, UNIQUE(actor,request_id));
`

type BrokerIdentity struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Project       string `json:"project"`
	Machine       string `json:"machine"`
	Scope         string `json:"scope"`
}

func OpenBroker(path string, create bool, project, machine string) (*Store, error) {
	if create && (!safeID(project) || !safeID(machine)) {
		return nil, errors.New("broker creation requires valid project and machine identifiers")
	}
	s, err := openStore(path, create, 1280790355, brokerSchema)
	if err != nil {
		return nil, err
	}
	if create {
		_, err = s.db.Exec("INSERT INTO broker VALUES(?,?,?)", randomID(), project, machine)
	}
	if err == nil {
		_, err = s.brokerIdentity(context.Background())
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) brokerIdentity(ctx context.Context) (BrokerIdentity, error) {
	b := BrokerIdentity{SchemaVersion: 1, Scope: "machine"}
	err := s.db.QueryRowContext(ctx, "SELECT id,project,machine FROM broker").Scan(&b.ID, &b.Project, &b.Machine)
	return b, err
}
func (s *Store) BrokerInfo(ctx context.Context, token string) (BrokerIdentity, error) {
	if _, err := authenticate(ctx, s.db, token); err != nil {
		return BrokerIdentity{}, err
	}
	return s.brokerIdentity(ctx)
}

// Session labels and claimed task/scope are advisory observations, never locks.
type BrokerSession struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	TaskID     string    `json:"task_id,omitempty"`
	Agent      string    `json:"agent"`
	State      string    `json:"state"` // active, ended; expired is derived at read time
	Summary    string    `json:"summary,omitempty"`
	Refs       []string  `json:"refs,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
	ValidUntil time.Time `json:"valid_until"`
}

type BrokerCommand struct {
	SchemaVersion int      `json:"schema_version"`
	RequestID     string   `json:"request_id"`
	Action        string   `json:"action"` // state, message, ack
	SessionID     string   `json:"session_id"`
	TaskID        string   `json:"task_id,omitempty"`
	Agent         string   `json:"agent,omitempty"`
	State         string   `json:"state,omitempty"`
	TTLSeconds    int      `json:"ttl_seconds,omitempty"`
	ToSession     string   `json:"to_session,omitempty"`
	ToTask        string   `json:"to_task,omitempty"`
	ReplyTo       string   `json:"reply_to,omitempty"`
	Text          string   `json:"text,omitempty"`
	Refs          []string `json:"refs,omitempty"`
}

type BrokerReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	BrokerID      string `json:"broker_id"`
	ID            string `json:"id"`
	Sequence      int64  `json:"sequence"`
	Status        string `json:"status"` // locally_stored, acknowledged
}

type BrokerEvent struct {
	Sequence int64         `json:"sequence"`
	ID       string        `json:"id"`
	Actor    string        `json:"actor"`
	At       time.Time     `json:"at"`
	Command  BrokerCommand `json:"command"`
}

type BrokerQuery struct {
	After        int64  `json:"after"`
	BrokerID     string `json:"broker_id,omitempty"`
	SessionID    string `json:"session_id"`
	TaskID       string `json:"task_id,omitempty"`
	ReplyTo      string `json:"reply_to,omitempty"`
	WatchSession string `json:"watch_session,omitempty"`
	WaitSeconds  int    `json:"wait_seconds,omitempty"`
}

type BrokerFeed struct {
	SessionsTruncated bool            `json:"sessions_truncated"`
	Broker            BrokerIdentity  `json:"broker"`
	Events            []BrokerEvent   `json:"events"`
	Sessions          []BrokerSession `json:"sessions"`
	NextAfter         int64           `json:"next_after"`
	ObservedAt        time.Time       `json:"observed_at"`
	Reason            string          `json:"reason"` // changes, snapshot, timeout, session_ended, contact_expired
}

func safeID(s string) bool { return validID(s) && cleanString(s) == s }

func cleanBrokerCommand(c BrokerCommand) (BrokerCommand, error) {
	if c.SchemaVersion != 1 || !safeID(c.RequestID) || !safeID(c.SessionID) {
		return c, errors.New("schema_version 1 and valid request_id/session_id required")
	}
	for _, id := range []string{c.TaskID, c.Agent, c.ToSession, c.ToTask, c.ReplyTo} {
		if id != "" && !safeID(id) {
			return c, errors.New("invalid or sensitive identifier")
		}
	}
	if len(c.Text) > 16384 || len(c.Refs) > 32 {
		return c, errors.New("message exceeds text/reference bounds")
	}
	c.Text = cleanString(c.Text)
	for i, ref := range c.Refs {
		if strings.TrimSpace(ref) == "" || len(ref) > 1024 {
			return c, errors.New("invalid reference")
		}
		c.Refs[i] = cleanString(ref)
	}
	switch c.Action {
	case "state":
		if c.State != "active" && c.State != "ended" {
			return c, errors.New("state must be active or ended")
		}
		if c.Agent == "" || c.TTLSeconds < 30 || c.TTLSeconds > 1800 || c.ToSession != "" || c.ToTask != "" || c.ReplyTo != "" {
			return c, errors.New("state requires agent and ttl_seconds 30..1800; no message routing")
		}
	case "message", "ack":
		if c.State != "" || c.Agent != "" || c.TTLSeconds != 0 || (c.ToSession != "" && c.ToTask != "") {
			return c, errors.New("message requires at most one recipient and no state fields")
		}
		if c.Action == "message" && strings.TrimSpace(c.Text) == "" {
			return c, errors.New("message text required")
		}
		if c.Action == "ack" && (c.ReplyTo == "" || c.ToSession != "" || c.ToTask != "" || c.Text != "" || len(c.Refs) != 0) {
			return c, errors.New("ack requires only reply_to, session and optional task")
		}
	default:
		return c, errors.New("unknown broker action")
	}
	return c, nil
}

func loadSession(ctx context.Context, q queryer, id string) (BrokerSession, error) {
	var b []byte
	var s BrokerSession
	err := q.QueryRowContext(ctx, "SELECT body FROM sessions WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}
func loadBrokerEvent(ctx context.Context, q queryer, id string) (BrokerEvent, error) {
	var b []byte
	var e BrokerEvent
	err := q.QueryRowContext(ctx, "SELECT body FROM messages WHERE id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(b, &e)
	}
	return e, err
}

func (s *Store) BrokerExecute(ctx context.Context, token string, c BrokerCommand) (BrokerReceipt, error) {
	var empty BrokerReceipt
	raw, err := json.Marshal(c)
	if err != nil {
		return empty, err
	}
	if len(raw) > MaxCommandBytes {
		return empty, errors.New("command too large")
	}
	hash := digest(raw)
	if c.Refs != nil {
		refs := make([]string, len(c.Refs))
		copy(refs, c.Refs)
		c.Refs = refs
	}
	c, err = cleanBrokerCommand(c)
	if err != nil {
		return empty, err
	}
	identity, err := s.brokerIdentity(ctx)
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
	if !m.Can("work") {
		return empty, ErrForbidden
	}
	var oldHash string
	var oldReceipt []byte
	err = tx.QueryRowContext(ctx, "SELECT digest,receipt FROM messages WHERE actor=? AND request_id=?", m.ID, c.RequestID).Scan(&oldHash, &oldReceipt)
	if err == nil {
		if hash != oldHash {
			return empty, ErrConflict
		}
		err = json.Unmarshal(oldReceipt, &empty)
		return empty, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	previous, err := loadSession(ctx, tx, c.SessionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return empty, err
	}
	if err == nil && previous.Actor != m.ID {
		return empty, ErrForbidden
	}
	if c.Action != "state" && err != nil {
		return empty, errors.New("publish session state before messaging")
	}
	if err == nil && previous.State == "ended" && !(c.Action == "state" && c.State == "ended") {
		return empty, errors.New("ended session cannot publish; use a new session ID")
	}
	if c.Action != "state" && c.TaskID != previous.TaskID {
		return empty, errors.New("task_id must match session state")
	}
	now := s.now()
	if c.Action == "state" {
		if c.Text == "" && c.TaskID == previous.TaskID {
			c.Text = previous.Summary
		}
		if c.Refs == nil && c.TaskID == previous.TaskID {
			c.Refs = previous.Refs
		}
		session := BrokerSession{ID: c.SessionID, Actor: m.ID, TaskID: c.TaskID, Agent: c.Agent, State: c.State, Summary: c.Text, Refs: c.Refs, UpdatedAt: now, ValidUntil: now.Add(time.Duration(c.TTLSeconds) * time.Second)}
		body, _ := json.Marshal(session)
		if len(body) > 32<<10 {
			return empty, errors.New("session state exceeds 32 KiB")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO sessions VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", c.SessionID, m.ID, string(body)); err != nil {
			return empty, err
		}
	}
	if c.ReplyTo != "" {
		parent, e := loadBrokerEvent(ctx, tx, c.ReplyTo)
		if e != nil {
			return empty, e
		}
		if parent.Command.Action != "message" {
			return empty, errors.New("reply_to must name a message")
		}
		if parent.Command.ToSession != "" && parent.Command.ToSession != c.SessionID || parent.Command.ToTask != "" && parent.Command.ToTask != c.TaskID {
			return empty, ErrForbidden
		}
		if c.ToSession != "" || c.ToTask != "" {
			return empty, errors.New("replies route to the original sender; omit recipients")
		}
		if parent.Command.TaskID != "" {
			c.ToTask = parent.Command.TaskID
		} else {
			c.ToSession = parent.Command.SessionID
		}
	}
	if c.ToSession != "" {
		if _, err = loadSession(ctx, tx, c.ToSession); err != nil {
			return empty, errors.New("recipient session not registered")
		}
	}
	// The ID is stable across retry and broker restart, and globally names its origin.
	id := identity.ID + "-" + randomID()
	result, err := tx.ExecContext(ctx, "INSERT INTO messages(id,actor,request_id,digest,body,receipt) VALUES(?,?,?,?,?,?)", id, m.ID, c.RequestID, hash, "", " ")
	if err != nil {
		return empty, err
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return empty, err
	}
	event := BrokerEvent{Sequence: seq, ID: id, Actor: m.ID, At: now, Command: c}
	receipt := BrokerReceipt{SchemaVersion: 1, BrokerID: identity.ID, ID: id, Sequence: seq, Status: "locally_stored"}
	if c.Action == "ack" {
		receipt.Status = "acknowledged"
	}
	body, _ := json.Marshal(event)
	if len(body) > 40<<10 {
		return empty, errors.New("event exceeds 40 KiB")
	}
	rb, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, "UPDATE messages SET body=?,receipt=? WHERE sequence=?", string(body), string(rb), seq); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return receipt, nil
}

// BrokerPull re-queries from the same cursor until a change/condition or deadline.
// Waiting holds no transaction/connection, so writers and other processes proceed.
// Timed rechecks also see commits from another process; no in-memory wakeup is authority.
func (s *Store) BrokerPull(ctx context.Context, token string, q BrokerQuery) (BrokerFeed, error) {
	if q.After < 0 || q.WaitSeconds < 0 || q.WaitSeconds > 20 || !safeID(q.SessionID) {
		return BrokerFeed{}, errors.New("valid session_id, nonnegative after and wait_seconds 0..20 required")
	}
	for _, id := range []string{q.TaskID, q.ReplyTo, q.WatchSession} {
		if id != "" && !safeID(id) {
			return BrokerFeed{}, errors.New("invalid query identifier")
		}
	}
	until := time.Now().Add(time.Duration(q.WaitSeconds) * time.Second)
	for {
		out, err := s.brokerSnapshot(ctx, token, q)
		if err != nil {
			return out, err
		}
		if len(out.Events) > 0 {
			out.Reason = "changes"
			return out, nil
		}
		if q.WatchSession != "" {
			for _, session := range out.Sessions {
				if session.ID == q.WatchSession {
					if session.State == "ended" {
						out.Reason = "session_ended"
						return out, nil
					}
					if session.State == "expired" {
						out.Reason = "contact_expired"
						return out, nil
					}
				}
			}
		}
		if q.WaitSeconds == 0 {
			out.Reason = "snapshot"
			return out, nil
		}
		if !time.Now().Before(until) {
			out.Reason = "timeout"
			return out, nil
		}
		// Advance only across events excluded by this exact query.
		q.After = out.NextAfter
		q.BrokerID = out.Broker.ID
		timer := time.NewTimer(min(200*time.Millisecond, time.Until(until)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return BrokerFeed{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) brokerSnapshot(ctx context.Context, token string, q BrokerQuery) (BrokerFeed, error) {
	identity, err := s.brokerIdentity(ctx)
	if err != nil {
		return BrokerFeed{}, err
	}
	out := BrokerFeed{Broker: identity, Events: []BrokerEvent{}, Sessions: []BrokerSession{}, NextAfter: q.After, ObservedAt: s.now()}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	m, err := authenticate(ctx, tx, token)
	if err != nil {
		return out, err
	}
	own, err := loadSession(ctx, tx, q.SessionID)
	if err != nil {
		return out, err
	}
	if own.Actor != m.ID {
		return out, ErrForbidden
	}
	if own.TaskID != q.TaskID {
		return out, errors.New("task_id must match session state")
	}
	var high int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM messages").Scan(&high); err != nil {
		return out, err
	}
	if q.After > 0 && q.BrokerID != identity.ID {
		return out, errors.New("cursor requires matching broker_id")
	}
	if q.BrokerID != "" && q.BrokerID != identity.ID {
		return out, errors.New("broker identity mismatch")
	}
	if q.After > high {
		return out, errors.New("cursor exceeds broker history; check broker identity")
	}
	// Filter in SQL so unrelated traffic cannot starve a waiter. A page ends at
	// its last event when full; otherwise it covers the snapshot's high watermark.
	rows, err := tx.QueryContext(ctx, `SELECT body FROM messages WHERE sequence>? AND sequence<=?
 AND (json_extract(body,'$.command.action')='state' OR
  (COALESCE(json_extract(body,'$.command.to_session'),'')='' AND COALESCE(json_extract(body,'$.command.to_task'),'')='') OR
  json_extract(body,'$.command.to_session')=? OR (?<>'' AND json_extract(body,'$.command.to_task')=?))
 AND (?='' OR json_extract(body,'$.command.reply_to')=?)
 AND (?='' OR (json_extract(body,'$.command.action')='state' AND json_extract(body,'$.command.session_id')=?))
 ORDER BY sequence LIMIT 100`, q.After, high, q.SessionID, q.TaskID, q.TaskID, q.ReplyTo, q.ReplyTo, q.WatchSession, q.WatchSession)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b []byte
		var event BrokerEvent
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &event); err != nil {
			break
		}
		out.Events = append(out.Events, event)
		out.NextAfter = event.Sequence
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if rowErr != nil {
		return out, rowErr
	}
	if len(out.Events) < 100 {
		out.NextAfter = high
	}
	rows, err = tx.QueryContext(ctx, "SELECT body FROM sessions WHERE (?='' OR id=?) ORDER BY json_extract(body,'$.updated_at') DESC,id LIMIT 257", q.WatchSession, q.WatchSession)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var session BrokerSession
		if err = rows.Scan(&b); err != nil {
			return out, err
		}
		if err = json.Unmarshal(b, &session); err != nil {
			return out, err
		}
		if session.State == "active" && !session.ValidUntil.After(out.ObservedAt) {
			session.State = "expired"
		}
		out.Sessions = append(out.Sessions, session)
	}
	if len(out.Sessions) > 256 {
		out.Sessions = out.Sessions[:256]
		out.SessionsTruncated = true
	}
	return out, rows.Err()
}

// ValidateBrokerCommand validates configured client identity without persisting.
func ValidateBrokerCommand(c BrokerCommand) error { _, err := cleanBrokerCommand(c); return err }
