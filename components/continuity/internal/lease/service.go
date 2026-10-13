// Package lease records leases: which agent holds a task, roadmap node, or
// track plan. A lease names a scope, a holder (a caller identity plus an
// optional session, on one instance), and an expiry, and renews explicitly.
// Every grant carries a token that only increases per scope, so a holder that
// lost its lease can tell. An expired lease frees itself. Holding one refuses
// nothing; fence.go stamps checkpoint and plan writes with the lease they were
// written under, and scopes the project marks required refuse those writes
// without a current lease. Leases live in the device database; a project that
// opts in to cross-machine leases also keeps them on its memory remote
// (remote.go), and the device tables mirror it.
package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	SchemaVersion = 1
	maxIDBytes    = 256
	DefaultTTL    = 15 * time.Minute
	MinTTL        = 30 * time.Second
	MaxTTL        = 24 * time.Hour
	minMargin     = 5 * time.Second
	// DefaultSkew is how long after another instance's lease expires before
	// this machine may take its scope, allowing for clock differences.
	DefaultSkew = 30 * time.Second
	Disclaimer  = "Leases are advisory unless config.toml [leases] required names the scope: they say who is working on a scope, and only required scopes refuse checkpoint or plan writes made without a current one. They are local to this machine unless [leases] remote shares them through the memory repository. Treat a lease as yours only until valid_until, and acquire again (a new token) once it has lapsed."
)

// ScopeKinds are the scopes a lease can hold.
var ScopeKinds = []string{"task", "roadmap_node", "track_plan"}

// Lease states.
const (
	Held     = "held"
	Expired  = "expired"
	Released = "released"
)

// ErrNotHeld means the caller does not hold the lease it tried to renew or
// release: the token is not the scope's latest grant, the holder differs, or
// (for renew) the lease already lapsed.
var ErrNotHeld = errors.New("lease not held")

// ConflictError is returned when another holder has a current lease, or one
// that expired on another instance within the takeover grace. Instance is the
// caller's.
type ConflictError struct {
	Current  Lease
	Instance string
}

func (e *ConflictError) Error() string {
	if e.Current.State == Expired {
		return fmt.Sprintf("%s was leased by %s (token %d) until %s; another machine's lease frees its scope only after the takeover grace", e.Current.Scope(), holderOn(e.Current, e.Instance), e.Current.Token, e.Current.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%s is leased by %s (token %d) until %s", e.Current.Scope(), holderOn(e.Current, e.Instance), e.Current.Token, e.Current.ExpiresAt.UTC().Format(time.RFC3339))
}

// holderOn names the lease's holder, adding its instance when that is not
// instance (the caller's).
func holderOn(value Lease, instance string) string {
	if value.InstanceID != "" && value.InstanceID != instance {
		return value.HolderLabel() + " on " + value.InstanceID
	}
	return value.HolderLabel()
}

// Lease is one grant with its latest event applied.
type Lease struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	ScopeKind  string     `json:"scope_kind"`
	ScopeID    string     `json:"scope_id"`
	Token      int64      `json:"token"`
	Holder     string     `json:"holder"`
	SessionID  string     `json:"session_id,omitempty"`
	InstanceID string     `json:"instance_id,omitempty"`
	AcquiredAt time.Time  `json:"acquired_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	// ValidUntil is ExpiresAt minus the safety margin measured when the lease
	// was last acquired or renewed; the holder should stop relying on it then.
	ValidUntil time.Time `json:"valid_until"`
	Renewals   int       `json:"renewals"`
	State      string    `json:"state"`
	// OnRemote reports that the grant's latest event is on the memory remote.
	// With cross-machine leases, a grant written here without it is
	// local-only until a sync publishes it.
	OnRemote bool `json:"on_remote,omitempty"`
}

// Scope renders the lease scope as KIND:ID.
func (l Lease) Scope() string { return l.ScopeKind + ":" + l.ScopeID }

// HolderLabel names the holder and, when given, its session.
func (l Lease) HolderLabel() string {
	if l.SessionID != "" {
		return l.Holder + " (session " + l.SessionID + ")"
	}
	return l.Holder
}

// Request asks for a lease, or renews or releases one.
type Request struct {
	ProjectID string
	ScopeKind string
	ScopeID   string
	Holder    string
	SessionID string
	// Token names the grant to renew or release; unused by Acquire.
	Token int64
	// TTL is how long the lease lasts from now; zero means DefaultTTL.
	TTL time.Duration
}

// Result is an acquired, renewed, or released lease. Renewed is true when
// Acquire found the caller already holding the scope and extended that grant.
// Warning explains a lease kept only on this machine because the memory remote
// could not be reached.
type Result struct {
	Lease   Lease  `json:"lease"`
	Renewed bool   `json:"renewed,omitempty"`
	Warning string `json:"warning,omitempty"`
}

type Service struct {
	DB  *sql.DB
	Now func() time.Time
	// Remote, when set, shares leases across machines through the memory
	// repository; nil keeps them on this machine.
	Remote Remote
	// Skew is the takeover grace after another instance's lease expires; zero
	// means DefaultSkew.
	Skew time.Duration
	// Policy names the required scopes, which refuse a local-only lease when
	// the remote cannot be reached.
	Policy Policy
}

func (s Service) skew() time.Duration {
	if s.Skew > 0 {
		return s.Skew
	}
	return DefaultSkew
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// ParseScope splits "kind:id" into a lease scope.
func ParseScope(ref string) (string, string, error) {
	kind, id, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || !oneOf(kind, ScopeKinds...) || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("lease scope %q must be task:ID, roadmap_node:ID, or track_plan:ID", ref)
	}
	return kind, strings.TrimSpace(id), nil
}

func plain(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("lease %s is required", name)
	}
	if len(value) > maxIDBytes || redact.String(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("lease %s must be a plain identifier of at most %d bytes", name, maxIDBytes)
	}
	return value, nil
}

func validate(request Request, needTTL bool) (Request, error) {
	if !oneOf(request.ScopeKind, ScopeKinds...) {
		return Request{}, fmt.Errorf("lease scope must be task, roadmap_node, or track_plan, not %q", request.ScopeKind)
	}
	var err error
	if request.ScopeID, err = plain("scope ID", request.ScopeID, true); err != nil {
		return Request{}, err
	}
	if request.Holder, err = plain("holder", request.Holder, true); err != nil {
		return Request{}, err
	}
	if request.SessionID, err = plain("session ID", request.SessionID, false); err != nil {
		return Request{}, err
	}
	if needTTL {
		if request.TTL == 0 {
			request.TTL = DefaultTTL
		}
		if request.TTL < MinTTL || request.TTL > MaxTTL {
			return Request{}, fmt.Errorf("lease TTL must be between %s and %s", MinTTL, MaxTTL)
		}
	}
	return request, nil
}

// margin is how long before expiry the holder should stop relying on a lease:
// a tenth of the TTL, at least minMargin and twice the time this write took
// (a locally measured bound on how late the holder can learn of its lease),
// and at most half the TTL.
func margin(ttl, elapsed time.Duration) time.Duration {
	value := ttl / 10
	if value < minMargin {
		value = minMargin
	}
	if value < 2*elapsed {
		value = 2 * elapsed
	}
	if value > ttl/2 {
		value = ttl / 2
	}
	return value
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// leaseSelect joins each grant with its latest event and its renewal count.
const leaseSelect = `SELECT g.id,g.scope_kind,g.scope_id,g.token,g.holder,g.session_id,g.instance_id,g.acquired_at,e.action,e.expires_at,e.margin_ms,e.recorded_at,e.sequence,
EXISTS(SELECT 1 FROM lease_remote_events r WHERE r.grant_id=g.id AND r.sequence=e.sequence)
FROM lease_grants g JOIN lease_events e ON e.grant_id=g.id AND e.sequence=(SELECT MAX(sequence) FROM lease_events WHERE grant_id=g.id)`

// latestGrant restricts leaseSelect to the newest grant of each scope.
const latestGrant = ` AND g.token=(SELECT MAX(token) FROM lease_grants WHERE project_id=g.project_id AND scope_kind=g.scope_kind AND scope_id=g.scope_id)`

func scan(row interface{ Scan(...any) error }, projectID string, now time.Time) (Lease, error) {
	value := Lease{ProjectID: projectID}
	var acquired, action, expires, recorded string
	var marginMS int64
	var sequence int
	if err := row.Scan(&value.ID, &value.ScopeKind, &value.ScopeID, &value.Token, &value.Holder, &value.SessionID, &value.InstanceID, &acquired, &action, &expires, &marginMS, &recorded, &sequence, &value.OnRemote); err != nil {
		return Lease{}, err
	}
	var err error
	if value.AcquiredAt, err = time.Parse(time.RFC3339Nano, acquired); err != nil {
		return Lease{}, err
	}
	if value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return Lease{}, err
	}
	value.ValidUntil = value.ExpiresAt.Add(-time.Duration(marginMS) * time.Millisecond)
	value.Renewals = sequence - 1
	switch {
	case action == "release":
		at, err := time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return Lease{}, err
		}
		value.ReleasedAt, value.Renewals, value.State = &at, sequence-2, Released
	case !now.Before(value.ExpiresAt):
		value.State = Expired
	default:
		value.State = Held
	}
	return value, nil
}

// CurrentLease returns the latest grant of a scope with its state at now, or nil.
func CurrentLease(ctx context.Context, db queryer, projectID, scopeKind, scopeID string, now time.Time) (*Lease, error) {
	return current(ctx, db, projectID, scopeKind, scopeID, now)
}

func current(ctx context.Context, db queryer, projectID, scopeKind, scopeID string, now time.Time) (*Lease, error) {
	value, err := scan(db.QueryRowContext(ctx, leaseSelect+` WHERE g.project_id=? AND g.scope_kind=? AND g.scope_id=?`+latestGrant, projectID, scopeKind, scopeID), projectID, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// sameHolder reports whether the request, made on instance, is the lease's
// holder. The same holder name on another machine is someone else.
func sameHolder(value Lease, request Request, instance string) bool {
	return value.Holder == request.Holder && value.SessionID == request.SessionID && value.InstanceID == instance
}

// blocks reports whether value keeps others out of its scope at now: it is
// held, or it expired on another instance less than skew ago.
func blocks(value *Lease, instance string, now time.Time, skew time.Duration) bool {
	if value == nil || value.State == Released {
		return false
	}
	if value.State == Held {
		return true
	}
	return value.InstanceID != instance && now.Before(value.ExpiresAt.Add(skew))
}

func instanceID(ctx context.Context, tx queryer, projectID string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT instance_id FROM project_memory_identities WHERE project_id=?`, projectID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// write runs fn in a transaction that takes the database write lock first, so
// reading the scope's latest grant and writing the next one is one atomic
// compare-and-set across processes.
func (s Service) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE lease_grants SET id=id WHERE 0`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func insertEvent(ctx context.Context, tx *sql.Tx, grantID string, sequence int, action string, expires time.Time, marginValue time.Duration, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO lease_events(id,grant_id,sequence,action,expires_at,margin_ms,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?)`,
		activity.NewID(), grantID, sequence, action, expires.UTC().Format(time.RFC3339Nano), marginValue.Milliseconds(), at.UTC().Format(time.RFC3339Nano), SchemaVersion)
	return err
}

// extend appends an acquire or renew event that makes value expire ttl from
// now, and updates value to match.
func extend(ctx context.Context, tx *sql.Tx, value *Lease, ttl time.Duration, now, started time.Time, action string) error {
	marginValue := margin(ttl, time.Since(started))
	value.ExpiresAt = now.Add(ttl)
	value.ValidUntil = value.ExpiresAt.Add(-marginValue)
	value.Renewals++
	value.State = Held
	return insertEvent(ctx, tx, value.ID, value.Renewals+1, action, value.ExpiresAt, marginValue, now)
}

// Acquire grants the scope to the caller under the next token when nobody
// holds it. When the caller already holds it, the lease is renewed under the
// same token. When someone else holds it, a *ConflictError names them; a lease
// that expired on another instance keeps its scope for the takeover grace.
func (s Service) Acquire(ctx context.Context, request Request) (Result, error) {
	request, err := validate(request, true)
	if err != nil {
		return Result{}, err
	}
	if s.Remote != nil {
		return s.acquireRemote(ctx, request)
	}
	return s.acquireLocal(ctx, request)
}

func (s Service) acquireLocal(ctx context.Context, request Request) (Result, error) {
	started := time.Now()
	var result Result
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.now()
		existing, err := current(ctx, tx, request.ProjectID, request.ScopeKind, request.ScopeID, now)
		if err != nil {
			return err
		}
		instance, err := instanceID(ctx, tx, request.ProjectID)
		if err != nil {
			return err
		}
		if existing != nil && existing.State == Held && sameHolder(*existing, request, instance) {
			result = Result{Lease: *existing, Renewed: true}
			return extend(ctx, tx, &result.Lease, request.TTL, now, started, "renew")
		}
		if blocks(existing, instance, now, s.skew()) {
			return &ConflictError{Current: *existing, Instance: instance}
		}
		var token int64 = 1
		if existing != nil {
			token = existing.Token + 1
		}
		result.Lease = Lease{ID: activity.NewID(), ProjectID: request.ProjectID, ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, Token: token, Holder: request.Holder, SessionID: request.SessionID, InstanceID: instance, AcquiredAt: now, Renewals: -1}
		if _, err := tx.ExecContext(ctx, `INSERT INTO lease_grants(id,project_id,scope_kind,scope_id,token,holder,session_id,instance_id,acquired_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			result.Lease.ID, result.Lease.ProjectID, result.Lease.ScopeKind, result.Lease.ScopeID, token, result.Lease.Holder, result.Lease.SessionID, instance, now.Format(time.RFC3339Nano), SchemaVersion); err != nil {
			return err
		}
		return extend(ctx, tx, &result.Lease, request.TTL, now, started, "acquire")
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// held loads the scope's latest grant and checks that the caller holds it
// under request.Token. A lapsed lease counts as held only when allowLapsed.
func held(ctx context.Context, tx queryer, request Request, now time.Time, allowLapsed bool) (*Lease, error) {
	if request.Token < 1 {
		return nil, errors.New("the token of the held grant is required")
	}
	existing, err := current(ctx, tx, request.ProjectID, request.ScopeKind, request.ScopeID, now)
	if err != nil {
		return nil, err
	}
	instance, err := instanceID(ctx, tx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	scope := request.ScopeKind + ":" + request.ScopeID
	switch {
	case existing == nil:
		return nil, fmt.Errorf("%w: %s has never been leased", ErrNotHeld, scope)
	case existing.Token != request.Token:
		return nil, fmt.Errorf("%w: %s is at token %d, not %d; acquire it again", ErrNotHeld, scope, existing.Token, request.Token)
	case !sameHolder(*existing, request, instance):
		return nil, fmt.Errorf("%w: token %d on %s belongs to %s", ErrNotHeld, request.Token, scope, holderOn(*existing, instance))
	case existing.State == Released:
		return nil, fmt.Errorf("%w: token %d on %s was released; acquire it again", ErrNotHeld, request.Token, scope)
	case existing.State == Expired && !allowLapsed:
		return nil, fmt.Errorf("%w: token %d on %s expired at %s; acquire it again for a new token", ErrNotHeld, request.Token, scope, existing.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return existing, nil
}

// Renew extends the caller's current lease under the same token. A lapsed
// lease cannot be renewed, since another holder may have relied on it being
// free; acquire it again for a new token instead.
func (s Service) Renew(ctx context.Context, request Request) (Result, error) {
	request, err := validate(request, true)
	if err != nil {
		return Result{}, err
	}
	if s.Remote != nil {
		return s.changeRemote(ctx, request, "renew")
	}
	value, err := s.renewLocal(ctx, request)
	return Result{Lease: value}, err
}

func (s Service) renewLocal(ctx context.Context, request Request) (Lease, error) {
	started := time.Now()
	var value Lease
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.now()
		existing, err := held(ctx, tx, request, now, false)
		if err != nil {
			return err
		}
		value = *existing
		return extend(ctx, tx, &value, request.TTL, now, started, "renew")
	})
	return value, err
}

// Release ends the caller's lease so the scope is free at once. Releasing a
// lease that already lapsed (and was not taken since) is allowed.
func (s Service) Release(ctx context.Context, request Request) (Result, error) {
	request, err := validate(request, false)
	if err != nil {
		return Result{}, err
	}
	if s.Remote != nil {
		return s.changeRemote(ctx, request, "release")
	}
	value, err := s.releaseLocal(ctx, request)
	return Result{Lease: value}, err
}

func (s Service) releaseLocal(ctx context.Context, request Request) (Lease, error) {
	var value Lease
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.now()
		existing, err := held(ctx, tx, request, now, true)
		if err != nil {
			return err
		}
		value = *existing
		if err := insertEvent(ctx, tx, value.ID, value.Renewals+2, "release", value.ExpiresAt, 0, now); err != nil {
			return err
		}
		value.ReleasedAt, value.State = &now, Released
		return nil
	})
	return value, err
}

// List returns the latest grant of each scope, held leases only unless all is
// set, ordered by scope.
func (s Service) List(ctx context.Context, projectID string, all bool) ([]Lease, error) {
	return list(ctx, s.DB, projectID, all, s.now())
}

func list(ctx context.Context, db queryer, projectID string, all bool, now time.Time) ([]Lease, error) {
	rows, err := db.QueryContext(ctx, leaseSelect+` WHERE g.project_id=?`+latestGrant+` ORDER BY g.scope_kind,g.scope_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Lease{}
	for rows.Next() {
		value, err := scan(rows, projectID, now)
		if err != nil {
			return nil, err
		}
		if all || value.State == Held {
			result = append(result, value)
		}
	}
	return result, rows.Err()
}

// History returns up to limit grants of one scope, newest (highest token)
// first.
func (s Service) History(ctx context.Context, projectID, scopeKind, scopeID string, limit int) ([]Lease, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, leaseSelect+` WHERE g.project_id=? AND g.scope_kind=? AND g.scope_id=? ORDER BY g.token DESC LIMIT ?`, projectID, scopeKind, scopeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now, result := s.now(), []Lease{}
	for rows.Next() {
		value, err := scan(rows, projectID, now)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// SessionTasks returns the tasks a session holds task leases on, most recently
// acquired first, for session selection. An empty session holds nothing.
func SessionTasks(ctx context.Context, db queryer, projectID, sessionID string, now time.Time) ([]string, error) {
	if sessionID == "" {
		return nil, nil
	}
	leases, err := list(ctx, db, projectID, false, now)
	if err != nil {
		return nil, err
	}
	var held []Lease
	for _, value := range leases {
		if value.ScopeKind == "task" && value.SessionID == sessionID {
			held = append(held, value)
		}
	}
	sort.SliceStable(held, func(i, j int) bool { return held[i].AcquiredAt.After(held[j].AcquiredAt) })
	tasks := make([]string, 0, len(held))
	for _, value := range held {
		tasks = append(tasks, value.ScopeID)
	}
	return tasks, nil
}

// maxNoticeLeases bounds how many leases the bootstrap line names.
const maxNoticeLeases = 3

// Notice describes, for the bootstrap, the held leases on a task and the
// roadmap nodes (nodes lists the task's, which the caller reads from pins) and track plans it belongs to, then counts the others. It
// returns "" when nothing is leased.
func Notice(ctx context.Context, db queryer, projectID, taskID string, nodes []string, now time.Time) (string, error) {
	leases, err := list(ctx, db, projectID, false, now)
	if err != nil || len(leases) == 0 {
		return "", err
	}
	related := map[string]bool{}
	if taskID != "" {
		related["task:"+taskID] = true
		for _, node := range nodes {
			related["roadmap_node:"+node] = true
		}
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT id FROM track_plan_revisions WHERE project_id=? AND task_id=?`, projectID, taskID)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var plan string
			if err := rows.Scan(&plan); err != nil {
				rows.Close()
				return "", err
			}
			related["track_plan:"+plan] = true
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	}
	var mine []Lease
	for _, value := range leases {
		if related[value.Scope()] {
			mine = append(mine, value)
		}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].ScopeKind == "task" && mine[j].ScopeKind != "task" })
	parts := []string{}
	for index, value := range mine {
		if index == maxNoticeLeases {
			break
		}
		parts = append(parts, fmt.Sprintf("%s is leased by %s (token %d, expires %s)", value.Scope(), value.HolderLabel(), value.Token, value.ExpiresAt.UTC().Format("15:04Z")))
	}
	text := strings.Join(parts, "; ")
	if extra := len(mine) - len(parts); extra > 0 {
		text += fmt.Sprintf("; %d more on this task's scopes", extra)
	}
	if others := len(leases) - len(mine); others > 0 {
		if text != "" {
			text += ". "
		}
		noun := "leases"
		if others == 1 {
			noun = "lease"
		}
		text += fmt.Sprintf("%d other held %s", others, noun)
	}
	return text + " (llewm lease list). Advisory; coordinate before working on a scope someone else holds.", nil
}
