package presence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

const (
	SchemaVersion = 1
	DefaultTTL    = 10 * time.Minute
	DefaultLimit  = 50
	MaxLimit      = 200
	refreshFloor  = 30 * time.Second
	Disclaimer    = "Presence is a short-lived device-local observation, not a durable task, lease, lock, or proof that a process still exists. Paths never enter portable memory."
)

var states = map[string]bool{
	"planning": true, "active": true, "yielded": true,
	"completed": true, "cancelled": true, "expired": true,
}

type Event struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ClaimID       string    `json:"claim_id"`
	Sequence      int       `json:"sequence"`
	Actor         string    `json:"actor"`
	Client        string    `json:"client"`
	SessionID     string    `json:"session_id"`
	InvocationID  string    `json:"invocation_id,omitempty"`
	TaskID        string    `json:"task_id,omitempty"`
	RoadmapNodeID string    `json:"roadmap_node_id,omitempty"`
	State         string    `json:"state"`
	StartedAt     time.Time `json:"started_at"`
	ObservedAt    time.Time `json:"observed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Authority     string    `json:"authority"`
	CheckoutPath  string    `json:"checkout_path,omitempty"`
	WorktreePath  string    `json:"worktree_path,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	DerivedState  string    `json:"derived_state,omitempty"`
	Overlap       []string  `json:"overlap,omitempty"`
}

func (e Event) CurrentState(now time.Time) string {
	if (e.State == "planning" || e.State == "active" || e.State == "yielded") && !e.ExpiresAt.After(now) {
		return "expired"
	}
	return e.State
}

func (e Event) Live(now time.Time) bool {
	state := e.CurrentState(now)
	return state == "planning" || state == "active"
}

type Observation struct {
	ProjectID     string
	ClaimID       string
	Actor         string
	Client        string
	SessionID     string
	InvocationID  string
	TaskID        string
	RoadmapNodeID string
	State         string
	Authority     string
	CheckoutPath  string
	WorktreePath  string
	Branch        string
	At            time.Time
	TTL           time.Duration
}

type Query struct {
	SessionID     string
	TaskID        string
	RoadmapNode   string
	CheckoutPath  string
	WorktreePath  string
	Branch        string
	State         string
	LiveOnly      bool
	History       bool
	Limit         int
	Now           time.Time
	FocusSession  string
	FocusTask     string
	FocusRoadmap  string
	FocusWorktree string
	FocusBranch   string
}

type Report struct {
	ProjectID  string  `json:"project_id"`
	Presence   []Event `json:"presence"`
	Total      int     `json:"total"`
	Truncated  bool    `json:"truncated"`
	Authority  string  `json:"authority"`
	Disclaimer string  `json:"disclaimer"`
}

type Snapshot struct {
	Report    Report     `json:"report"`
	Worktrees []Worktree `json:"worktrees,omitempty"`
}

type Service struct{ DB *sql.DB }

func ClaimID(projectID, client, sessionID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + client + "\x00" + sessionID))
	return "presence-" + hex.EncodeToString(sum[:12])
}

func (s Service) Observe(ctx context.Context, value Observation) (Event, bool, error) {
	value.ProjectID = strings.TrimSpace(value.ProjectID)
	value.Client = strings.TrimSpace(value.Client)
	value.SessionID = strings.TrimSpace(value.SessionID)
	value.State = strings.ToLower(strings.TrimSpace(value.State))
	if value.ProjectID == "" || value.Client == "" || value.SessionID == "" {
		return Event{}, false, errors.New("presence needs project, client, and session")
	}
	if !states[value.State] {
		return Event{}, false, fmt.Errorf("invalid presence state %q", value.State)
	}
	if value.ClaimID == "" {
		value.ClaimID = ClaimID(value.ProjectID, value.Client, value.SessionID)
	}
	if value.Actor == "" {
		value.Actor = value.Client
	}
	if value.Authority == "" {
		value.Authority = "machine_observed"
	}
	if value.Authority != "machine_observed" && value.Authority != "caller_asserted" && value.Authority != "user_reported" {
		return Event{}, false, fmt.Errorf("invalid presence authority %q", value.Authority)
	}
	if value.At.IsZero() {
		value.At = time.Now().UTC()
	} else {
		value.At = value.At.UTC()
	}
	if value.TTL <= 0 {
		value.TTL = DefaultTTL
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, false, err
	}
	defer tx.Rollback()
	previous, err := latest(ctx, tx, value.ProjectID, value.ClaimID)
	if err != nil {
		return Event{}, false, err
	}
	if previous != nil && sameObservation(*previous, value) && value.At.Sub(previous.ObservedAt) < refreshFloor {
		return *previous, false, nil
	}
	started := value.At
	sequence := 1
	if previous != nil {
		sequence = previous.Sequence + 1
		started = previous.StartedAt
		if terminal(previous.State) && !terminal(value.State) {
			started = value.At
		}
	}
	expires := value.At
	if !terminal(value.State) {
		expires = value.At.Add(value.TTL)
	}
	event := Event{
		ID: activity.NewID(), ProjectID: value.ProjectID, ClaimID: value.ClaimID, Sequence: sequence,
		Actor: value.Actor, Client: value.Client, SessionID: value.SessionID, InvocationID: value.InvocationID,
		TaskID: value.TaskID, RoadmapNodeID: value.RoadmapNodeID, State: value.State,
		StartedAt: started, ObservedAt: value.At, ExpiresAt: expires, Authority: value.Authority,
		CheckoutPath: value.CheckoutPath, WorktreePath: value.WorktreePath, Branch: value.Branch, SchemaVersion: SchemaVersion,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO presence_events(
        id,project_id,claim_id,sequence,actor,client,session_id,invocation_id,task_id,roadmap_node_id,state,
        started_at,observed_at,expires_at,authority,checkout_path,worktree_path,branch,schema_version
      ) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.ProjectID, event.ClaimID, event.Sequence,
		event.Actor, event.Client, event.SessionID, event.InvocationID, event.TaskID, event.RoadmapNodeID, event.State,
		format(event.StartedAt), format(event.ObservedAt), format(event.ExpiresAt), event.Authority,
		event.CheckoutPath, event.WorktreePath, event.Branch, event.SchemaVersion); err != nil {
		return Event{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, false, err
	}
	return event, true, nil
}

func (s Service) Expire(ctx context.Context, projectID string, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	report, err := s.List(ctx, projectID, Query{Limit: MaxLimit, Now: now})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, current := range report.Presence {
		if current.CurrentState(now) != "expired" || current.State == "expired" {
			continue
		}
		_, inserted, err := s.Observe(ctx, Observation{
			ProjectID: current.ProjectID, ClaimID: current.ClaimID, Actor: current.Actor, Client: current.Client,
			SessionID: current.SessionID, InvocationID: current.InvocationID, TaskID: current.TaskID,
			RoadmapNodeID: current.RoadmapNodeID, State: "expired", Authority: "machine_observed",
			CheckoutPath: current.CheckoutPath, WorktreePath: current.WorktreePath, Branch: current.Branch, At: now,
		})
		if err != nil {
			return count, err
		}
		if inserted {
			count++
		}
	}
	return count, nil
}

func (s Service) List(ctx context.Context, projectID string, query Query) (Report, error) {
	if query.Limit <= 0 {
		query.Limit = DefaultLimit
	}
	if query.Limit > MaxLimit {
		return Report{}, fmt.Errorf("presence limit must be at most %d", MaxLimit)
	}
	if query.Now.IsZero() {
		query.Now = time.Now().UTC()
	}
	filters := []string{}
	filterArgs := []any{}
	for column, value := range map[string]string{
		"session_id": query.SessionID, "task_id": query.TaskID, "roadmap_node_id": query.RoadmapNode,
		"checkout_path": query.CheckoutPath, "worktree_path": query.WorktreePath, "branch": query.Branch,
	} {
		if value != "" {
			filters = append(filters, column+"=?")
			filterArgs = append(filterArgs, value)
		}
	}
	if query.State != "" && !states[query.State] {
		return Report{}, fmt.Errorf("invalid presence state %q", query.State)
	}
	if query.State != "" {
		switch query.State {
		case "expired":
			filters = append(filters, "(state='expired' OR (state IN ('planning','active','yielded') AND julianday(expires_at) <= julianday(?)))")
			filterArgs = append(filterArgs, format(query.Now))
		case "planning", "active", "yielded":
			filters = append(filters, "state=? AND julianday(expires_at) > julianday(?)")
			filterArgs = append(filterArgs, query.State, format(query.Now))
		default:
			filters = append(filters, "state=?")
			filterArgs = append(filterArgs, query.State)
		}
	}
	if query.LiveOnly {
		filters = append(filters, "state IN ('planning','active') AND julianday(expires_at) > julianday(?)")
		filterArgs = append(filterArgs, format(query.Now))
	}
	columns := `id,project_id,claim_id,sequence,actor,client,session_id,invocation_id,task_id,roadmap_node_id,state,
        started_at,observed_at,expires_at,authority,checkout_path,worktree_path,branch,schema_version`
	args := []any{projectID}
	var base string
	if query.History {
		base = `SELECT ` + columns + ` FROM presence_events WHERE project_id=?`
	} else {
		base = `WITH ranked AS (SELECT *, ROW_NUMBER() OVER (PARTITION BY claim_id ORDER BY sequence DESC) AS rank FROM presence_events WHERE project_id=?) SELECT ` + columns + ` FROM ranked WHERE rank=1`
	}
	if len(filters) > 0 {
		base += " AND " + strings.Join(filters, " AND ")
		args = append(args, filterArgs...)
	}
	base += " ORDER BY observed_at DESC,id DESC LIMIT ?"
	args = append(args, query.Limit+1)
	rows, err := s.DB.QueryContext(ctx, base, args...)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	values := []Event{}
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return Report{}, err
		}
		value.DerivedState = value.CurrentState(query.Now)
		value.Overlap = overlap(value, query)
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return Report{}, err
	}
	truncated := len(values) > query.Limit
	if truncated {
		values = values[:query.Limit]
	}
	return Report{ProjectID: projectID, Presence: values, Total: len(values), Truncated: truncated, Authority: "machine_observed", Disclaimer: Disclaimer}, nil
}

func overlap(value Event, query Query) []string {
	if query.FocusSession != "" && value.SessionID == query.FocusSession {
		return []string{"same_session"}
	}
	var result []string
	if query.FocusTask != "" && value.TaskID != "" && value.TaskID == query.FocusTask {
		result = append(result, "same_task")
	}
	if query.FocusRoadmap != "" && value.RoadmapNodeID != "" && value.RoadmapNodeID == query.FocusRoadmap {
		result = append(result, "same_roadmap_node")
	}
	if query.FocusWorktree != "" && value.WorktreePath != "" && value.WorktreePath == query.FocusWorktree {
		result = append(result, "same_worktree")
	}
	if query.FocusBranch != "" && value.Branch != "" && value.Branch == query.FocusBranch {
		result = append(result, "same_branch")
	}
	if len(result) == 0 && (value.TaskID == "" || value.WorktreePath == "") && value.Live(query.Now) {
		result = append(result, "unknown_scope")
	}
	return result
}

func sameObservation(previous Event, value Observation) bool {
	return previous.State == value.State && previous.Actor == value.Actor && previous.Client == value.Client &&
		previous.SessionID == value.SessionID && previous.InvocationID == value.InvocationID && previous.TaskID == value.TaskID &&
		previous.RoadmapNodeID == value.RoadmapNodeID && previous.Authority == value.Authority &&
		previous.CheckoutPath == value.CheckoutPath && previous.WorktreePath == value.WorktreePath && previous.Branch == value.Branch
}

func terminal(state string) bool {
	return state == "completed" || state == "cancelled" || state == "expired"
}
func format(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

type scanner interface{ Scan(...any) error }

func latest(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, claimID string) (*Event, error) {
	row := q.QueryRowContext(ctx, `SELECT id,project_id,claim_id,sequence,actor,client,session_id,invocation_id,task_id,roadmap_node_id,state,
        started_at,observed_at,expires_at,authority,checkout_path,worktree_path,branch,schema_version
      FROM presence_events WHERE project_id=? AND claim_id=? ORDER BY sequence DESC LIMIT 1`, projectID, claimID)
	value, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}

func scan(row scanner) (Event, error) {
	var value Event
	var started, observed, expires string
	err := row.Scan(&value.ID, &value.ProjectID, &value.ClaimID, &value.Sequence, &value.Actor, &value.Client,
		&value.SessionID, &value.InvocationID, &value.TaskID, &value.RoadmapNodeID, &value.State,
		&started, &observed, &expires, &value.Authority, &value.CheckoutPath, &value.WorktreePath, &value.Branch, &value.SchemaVersion)
	if err != nil {
		return Event{}, err
	}
	for target, source := range map[*time.Time]string{&value.StartedAt: started, &value.ObservedAt: observed, &value.ExpiresAt: expires} {
		parsed, err := time.Parse(time.RFC3339Nano, source)
		if err != nil {
			return Event{}, err
		}
		*target = parsed
	}
	return value, nil
}
