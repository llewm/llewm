package checkpoint

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type Checkpoint struct {
	ID                      string         `json:"id"`
	ProjectID               string         `json:"project_id"`
	SessionID               string         `json:"session_id,omitempty"`
	RestartResume           *RestartResume `json:"restart_resume,omitempty"`
	TaskID                  string         `json:"task_id,omitempty"`
	PredecessorCheckpointID string         `json:"predecessor_checkpoint_id,omitempty"`
	// ReviewsTaskID names another task this task reviews or repairs. The link
	// shows in both tasks' bootstraps; it never changes the other task's chain.
	ReviewsTaskID string         `json:"reviews_task_id,omitempty"`
	RequestKey    string         `json:"-"`
	Task          string         `json:"task"`
	Status        string         `json:"status"`
	Summary       string         `json:"summary"`
	NextAction    string         `json:"next_action"`
	Completed     []string       `json:"completed,omitempty"`
	Attempts      []Attempt      `json:"attempts,omitempty"`
	Changes       []string       `json:"changes,omitempty"`
	Verified      []string       `json:"verified,omitempty"`
	EvidenceRefs  []string       `json:"evidence_refs,omitempty"`
	Authority     string         `json:"authority,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	// CodeHeads is each repository's HEAD when the checkpoint is written, set by
	// the caller that can read Git. It is stored as the checkpoint's context, not
	// in its payload.
	CodeHeads []CodeHead `json:"-"`
	// BasedOn, when set, is the checkpoint this one continues in place of the
	// task tip: a fork resolution continues from the accepted head.
	BasedOn string `json:"-"`
	// Fence is the lease the writer presents for the task's scope; a required
	// scope refuses the write without it.
	Fence lease.Fence `json:"-"`
}

// RestartResume scopes a caller-requested restart handoff to its originating
// local instance and client. Only the current task tip can be eligible.
type RestartResume struct {
	InstanceID string `json:"instance_id"`
	Client     string `json:"client"`
}

type Attempt struct {
	Description string `json:"description"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason,omitempty"`
}

type Action struct {
	ID                  string    `json:"id"`
	SourceCheckpointID  string    `json:"source_checkpoint_id"`
	Description         string    `json:"description"`
	Status              string    `json:"status"`
	CarriedToCheckpoint string    `json:"carried_to_checkpoint,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	// TaskID is the task of the checkpoint that created the action; empty for
	// actions of untasked checkpoints. Actions belong to that task only.
	TaskID string `json:"task_id,omitempty"`
}

type CreateResult struct {
	Checkpoint Checkpoint `json:"checkpoint"`
	// Lease says which lease the checkpoint was written under; nil when it has
	// no task to lease.
	Lease          *lease.Stamp `json:"lease,omitempty"`
	CarriedForward []Action     `json:"carried_forward"`
	Reconciled     []Action     `json:"reconciled"`
	ActionCreated  bool         `json:"action_created"`
	// SessionLease reports the advisory task lease taken, renewed, or released
	// for the checkpoint's session, or why none was taken.
	SessionLease string `json:"session_lease,omitempty"`
}

type ActionUpdate struct {
	ID     string `json:"id" jsonschema:"unresolved checkpoint action identifier"`
	Status string `json:"status" jsonschema:"completed or superseded"`
	Reason string `json:"reason,omitempty" jsonschema:"caller-supplied transition reason"`
}

type ActionReconciliation struct {
	Updates               []ActionUpdate `json:"updates,omitempty"`
	CarryForwardActionIDs []string       `json:"carry_forward_action_ids,omitempty"`
}

type ActionProjection struct {
	Current                  []Action `json:"current"`
	Remaining                []Action `json:"remaining"`
	Reconciled               []Action `json:"reconciled"`
	ExplicitlyCarried        []Action `json:"explicitly_carried"`
	CreatesNextAction        bool     `json:"creates_next_action"`
	ProjectedUnresolvedCount int      `json:"projected_unresolved_count"`
}

type Service struct{ DB *sql.DB }

type Selection struct {
	CheckpointID string `json:"checkpoint_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
}

const MaxBytes = 32 << 10

const (
	DefaultHistoryLimit  = 20
	MaxHistoryLimit      = 100
	DefaultExportBytes   = 16 << 10
	DefaultActivityLimit = 20
	MaxActivityLimit     = 100
	MaxReminderEvents    = 1_000_000
)

type ReminderPolicy struct {
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty"`
	MaxEvents     int   `json:"max_events,omitempty"`
}

type ActivityObservation struct {
	ID         string    `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	SessionID  string    `json:"session_id,omitempty"`
	TaskID     string    `json:"task_id,omitempty"`
	Client     string    `json:"client"`
	EventType  string    `json:"event_type"`
	Category   string    `json:"category"`
	Outcome    string    `json:"outcome,omitempty"`
}

type Freshness struct {
	SchemaVersion         int                   `json:"schema_version"`
	Checkpoint            *Checkpoint           `json:"checkpoint,omitempty"`
	Freshness             string                `json:"freshness"`
	AgeSeconds            *int64                `json:"age_seconds,omitempty"`
	ActivityScope         string                `json:"activity_scope"`
	EventsSince           int                   `json:"events_since_checkpoint"`
	EventsObserved        int                   `json:"events_observed,omitempty"`
	RecentActivity        []ActivityObservation `json:"recent_activity"`
	ActivityTruncated     bool                  `json:"activity_truncated"`
	RepositoryAuthority   string                `json:"repository_authority"`
	CheckpointAuthority   string                `json:"checkpoint_authority,omitempty"`
	ReminderEvaluated     bool                  `json:"reminder_evaluated"`
	CheckpointRecommended bool                  `json:"checkpoint_recommended"`
	ReminderReasons       []string              `json:"reminder_reasons"`
	ReminderPolicy        ReminderPolicy        `json:"reminder_policy"`
	Disclaimer            string                `json:"disclaimer"`
}

type ReminderEdge struct {
	Freshness     Freshness `json:"freshness"`
	ShouldSurface bool      `json:"should_surface"`
}

// EvaluateReminderEdge evaluates caller-owned policy for one session and
// atomically claims one advisory delivery per session and checkpoint. Age and
// activity count from the latest checkpoint of the session's task (taskID), or
// from the session's first observed event when it has none, and only this
// session's events count. Other agents' checkpoints and activity therefore
// neither reset nor trigger this session's reminder. The delivery record is a
// payload-free observation; it does not establish that a model saw the reminder.
func (s Service) EvaluateReminderEdge(ctx context.Context, projectID, client, sessionID, taskID string, policy ReminderPolicy) (ReminderEdge, error) {
	result := Freshness{SchemaVersion: 2, Freshness: "no_checkpoint", ActivityScope: "session", RecentActivity: []ActivityObservation{}, RepositoryAuthority: "machine_observed", ReminderEvaluated: policy.MaxAgeSeconds > 0 || policy.MaxEvents > 0, ReminderReasons: []string{}, ReminderPolicy: policy, Disclaimer: "Session-scoped observation after this session's task checkpoint; it does not infer task progress."}
	if sessionID == "" || !result.ReminderEvaluated {
		return ReminderEdge{Freshness: result}, nil
	}
	var latest *Checkpoint
	if strings.TrimSpace(taskID) != "" {
		var err error
		if latest, err = s.LatestSelected(ctx, projectID, Selection{TaskID: taskID}); err != nil {
			return ReminderEdge{}, err
		}
	}
	edge := "session-start"
	var since time.Time
	if latest != nil {
		edge, since, result.Checkpoint, result.Freshness = latest.ID, latest.CreatedAt, latest, "current"
		result.CheckpointAuthority = "caller_asserted"
	} else {
		var first sql.NullString
		if err := s.DB.QueryRowContext(ctx, `SELECT MIN(received_at) FROM events WHERE project_id=? AND session_id=?`, projectID, sessionID).Scan(&first); err != nil {
			return ReminderEdge{}, err
		}
		if !first.Valid {
			return ReminderEdge{Freshness: result}, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, first.String)
		if err != nil {
			return ReminderEdge{}, err
		}
		since = parsed
		result.ReminderReasons = append(result.ReminderReasons, "no_checkpoint")
	}
	age := max(int64(time.Since(since).Seconds()), 0)
	result.AgeSeconds = &age
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE project_id=? AND session_id=? AND received_at > ? AND event_type NOT IN ('checkpointreminderrequested')`, projectID, sessionID, since.Format(time.RFC3339Nano)).Scan(&result.EventsSince); err != nil {
		return ReminderEdge{}, err
	}
	if result.EventsSince > 0 && latest != nil {
		result.Freshness = "activity_since_checkpoint"
	}
	if policy.MaxAgeSeconds > 0 && age >= policy.MaxAgeSeconds {
		result.CheckpointRecommended = true
		result.ReminderReasons = append(result.ReminderReasons, "age_threshold")
	}
	if policy.MaxEvents > 0 && result.EventsSince >= policy.MaxEvents {
		result.CheckpointRecommended = true
		result.ReminderReasons = append(result.ReminderReasons, "event_threshold")
	}
	if !result.CheckpointRecommended {
		result.ReminderReasons = []string{}
		return ReminderEdge{Freshness: result}, nil
	}
	sum := sha256.Sum256([]byte(projectID + "\x00" + sessionID + "\x00" + edge))
	event := activity.NewEvent(projectID, client, "checkpointreminderrequested", "continuity")
	event.ID = "reminder-" + hex.EncodeToString(sum[:16])
	event.SessionID = sessionID
	event.ActorType = "local_system"
	event.Outcome = "requested"
	inserted, err := (activity.Store{DB: s.DB}).AppendOnce(ctx, event)
	return ReminderEdge{Freshness: result, ShouldSurface: inserted}, err
}

// SessionTask is the task a session last checkpointed, or "" when it has none.
func SessionTask(ctx context.Context, db rowQueryer, projectID, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", nil
	}
	var taskID string
	err := db.QueryRowContext(ctx, `SELECT task_id FROM checkpoints WHERE project_id=? AND session_id=? AND task_id IS NOT NULL AND task_id<>'' ORDER BY created_at DESC,id DESC LIMIT 1`, projectID, sessionID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

func (s Service) Freshness(ctx context.Context, projectID string, selection Selection, limit int, policy ReminderPolicy) (Freshness, error) {
	if limit == 0 {
		limit = DefaultActivityLimit
	}
	if limit < 1 || limit > MaxActivityLimit {
		return Freshness{}, fmt.Errorf("limit must be between 1 and %d", MaxActivityLimit)
	}
	if policy.MaxAgeSeconds < 0 {
		return Freshness{}, fmt.Errorf("max age seconds must not be negative")
	}
	if policy.MaxEvents < 0 || policy.MaxEvents > MaxReminderEvents {
		return Freshness{}, fmt.Errorf("max events must be between 0 and %d", MaxReminderEvents)
	}
	latest, err := s.LatestSelected(ctx, projectID, selection)
	if err != nil {
		return Freshness{}, err
	}
	result := Freshness{SchemaVersion: 2, Checkpoint: latest, Freshness: "no_checkpoint", ActivityScope: "project", RecentActivity: []ActivityObservation{}, RepositoryAuthority: "machine_observed", ReminderEvaluated: policy.MaxAgeSeconds > 0 || policy.MaxEvents > 0, ReminderReasons: []string{}, ReminderPolicy: policy, Disclaimer: "Activity is a bounded project-scoped observation after the selected caller-asserted checkpoint. Reminder recommendations apply only explicit caller thresholds; they do not infer task progress or create semantic state."}
	if latest == nil {
		result.Disclaimer = "No caller-asserted checkpoint matches the selection. Recent activity is bounded project-scoped machine observation; it cannot establish task intent, progress, completion, or a next action."
		if result.ReminderEvaluated {
			result.CheckpointRecommended = true
			result.ReminderReasons = append(result.ReminderReasons, "no_checkpoint")
		}
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE project_id = ?`, projectID).Scan(&result.EventsObserved); err != nil {
			return Freshness{}, err
		}
		result.ActivityTruncated = result.EventsObserved > limit
		rows, err := s.DB.QueryContext(ctx, `SELECT id, received_at, COALESCE(session_id, ''), COALESCE(task_id, ''), client, event_type, category, COALESCE(outcome, '') FROM events WHERE project_id = ? ORDER BY received_at DESC, id DESC LIMIT ?`, projectID, limit)
		if err != nil {
			return Freshness{}, err
		}
		defer rows.Close()
		result.RecentActivity, err = scanActivity(rows)
		return result, err
	}
	result.CheckpointAuthority = "caller_asserted"
	age := int64(time.Since(latest.CreatedAt).Seconds())
	if age < 0 {
		age = 0
	}
	result.AgeSeconds = &age
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE project_id = ? AND received_at > ?`, projectID, latest.CreatedAt.Format(time.RFC3339Nano)).Scan(&result.EventsSince); err != nil {
		return Freshness{}, err
	}
	result.Freshness = "current"
	if result.EventsSince > 0 {
		result.Freshness = "activity_since_checkpoint"
	}
	if policy.MaxAgeSeconds > 0 && age >= policy.MaxAgeSeconds {
		result.CheckpointRecommended = true
		result.ReminderReasons = append(result.ReminderReasons, "age_threshold")
	}
	if policy.MaxEvents > 0 && result.EventsSince >= policy.MaxEvents {
		result.CheckpointRecommended = true
		result.ReminderReasons = append(result.ReminderReasons, "event_threshold")
	}
	result.ActivityTruncated = result.EventsSince > limit
	rows, err := s.DB.QueryContext(ctx, `SELECT id, received_at, COALESCE(session_id, ''), COALESCE(task_id, ''), client, event_type, category, COALESCE(outcome, '') FROM events WHERE project_id = ? AND received_at > ? ORDER BY received_at DESC, id DESC LIMIT ?`, projectID, latest.CreatedAt.Format(time.RFC3339Nano), limit)
	if err != nil {
		return Freshness{}, err
	}
	defer rows.Close()
	result.RecentActivity, err = scanActivity(rows)
	return result, err
}

func scanActivity(rows *sql.Rows) ([]ActivityObservation, error) {
	result := make([]ActivityObservation, 0)
	for rows.Next() {
		var observation ActivityObservation
		var received string
		if err := rows.Scan(&observation.ID, &received, &observation.SessionID, &observation.TaskID, &observation.Client, &observation.EventType, &observation.Category, &observation.Outcome); err != nil {
			return nil, err
		}
		var err error
		observation.ReceivedAt, err = time.Parse(time.RFC3339Nano, received)
		if err != nil {
			return nil, fmt.Errorf("decode event time %s: %w", observation.ID, err)
		}
		result = append(result, observation)
	}
	return result, rows.Err()
}

type FindRequest struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type HistoryHit struct {
	ID        string    `json:"id"`
	Task      string    `json:"task"`
	Status    string    `json:"status"`
	Excerpt   string    `json:"excerpt"`
	Authority string    `json:"authority"`
	CreatedAt time.Time `json:"created_at"`
}

type FindResult struct {
	SchemaVersion int          `json:"schema_version"`
	Hits          []HistoryHit `json:"hits"`
	NextCursor    string       `json:"next_cursor,omitempty"`
	Authority     string       `json:"authority"`
	Disclaimer    string       `json:"disclaimer"`
}

type Export struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	Markdown      string    `json:"markdown"`
	MaxBytes      int       `json:"max_bytes"`
	Truncated     bool      `json:"truncated"`
	Authority     string    `json:"authority"`
	Disclaimer    string    `json:"disclaimer"`
}

// prepare bounds and sanitizes every field before either SQL columns or JSON
// are written. Round-tripping also avoids mutating the caller's slices/maps.
// Prepare validates, bounds, redacts, and assigns prospective identity metadata
// without persisting a checkpoint. Callers may use it for deterministic preflight;
// CreateWithActions prepares again inside the write path.
func Prepare(input Checkpoint) (Checkpoint, error) {
	input.Task = strings.TrimSpace(input.Task)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.PredecessorCheckpointID = strings.TrimSpace(input.PredecessorCheckpointID)
	input.ReviewsTaskID = strings.TrimSpace(input.ReviewsTaskID)
	requestKey := strings.TrimSpace(input.RequestKey)
	input.NextAction = strings.TrimSpace(input.NextAction)
	input.Status = strings.TrimSpace(input.Status)
	if input.ProjectID == "" || input.Task == "" || input.NextAction == "" {
		return Checkpoint{}, fmt.Errorf("checkpoint project, task and next action are required")
	}
	if len(requestKey) > 200 {
		return Checkpoint{}, fmt.Errorf("checkpoint request key exceeds 200 bytes")
	}
	if input.Status == "" {
		input.Status = "in_progress"
	}
	switch input.Status {
	case "in_progress", "completed", "blocked", "paused":
	default:
		return Checkpoint{}, fmt.Errorf("checkpoint status must be in_progress, completed, blocked or paused")
	}
	if input.RestartResume != nil && (input.Status != "in_progress" || input.SessionID == "" || input.RestartResume.InstanceID == "" || input.RestartResume.Client == "") {
		return Checkpoint{}, fmt.Errorf("restart resume requires an in-progress checkpoint and an identified session, client and instance")
	}
	for _, items := range [][]string{input.Completed, input.Changes, input.Verified, input.EvidenceRefs} {
		if len(items) > 64 {
			return Checkpoint{}, fmt.Errorf("checkpoint lists may contain at most 64 entries")
		}
		for _, item := range items {
			if strings.TrimSpace(item) == "" {
				return Checkpoint{}, fmt.Errorf("checkpoint list entries must not be blank")
			}
		}
	}
	if len(input.Attempts) > 64 {
		return Checkpoint{}, fmt.Errorf("checkpoint attempts may contain at most 64 entries")
	}
	for _, attempt := range input.Attempts {
		if strings.TrimSpace(attempt.Description) == "" || strings.TrimSpace(attempt.Outcome) == "" {
			return Checkpoint{}, fmt.Errorf("checkpoint attempts require a description and outcome")
		}
	}
	// No client can promote its own assertions to machine-observed authority.
	input.Authority = "caller_asserted"
	input.CreatedAt = time.Now().UTC()
	// time.Time JSON uses RFC3339Nano and trims trailing fractional zeros. Keep
	// the least-significant digit non-zero so prospective and persisted
	// checkpoint byte accounting cannot vary solely with timestamp formatting.
	if input.CreatedAt.Nanosecond()%10 == 0 {
		input.CreatedAt = input.CreatedAt.Add(time.Nanosecond)
	}
	if input.ID == "" {
		input.ID = activity.NewID()
	}
	body, err := json.Marshal(input)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	if len(body) > MaxBytes {
		return Checkpoint{}, fmt.Errorf("checkpoint exceeds 32 KiB limit")
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return Checkpoint{}, err
	}
	body, err = json.Marshal(redact.Value(value))
	if err != nil {
		return Checkpoint{}, err
	}
	if len(body) > MaxBytes {
		return Checkpoint{}, fmt.Errorf("redacted checkpoint exceeds 32 KiB limit")
	}
	var result Checkpoint
	if err := json.Unmarshal(body, &result); err != nil {
		return Checkpoint{}, err
	}
	result.RequestKey = requestKey
	// Code heads are machine-observed commit IDs and member paths; they skip the
	// payload round trip because they are not part of the payload.
	result.CodeHeads, result.BasedOn, result.Fence = input.CodeHeads, input.BasedOn, input.Fence
	return result, nil
}

func (s Service) Create(ctx context.Context, checkpoint Checkpoint) (Checkpoint, error) {
	result, err := s.CreateWithActions(ctx, checkpoint)
	return result.Checkpoint, err
}

// CreateWithActions records a checkpoint and carries every unresolved predecessor
// action forward. Nonterminal checkpoints also create a new open action; completed
// checkpoints are terminal. It never infers predecessor completion.
func (s Service) CreateWithActions(ctx context.Context, checkpoint Checkpoint) (CreateResult, error) {
	return s.CreateWithReconciliation(ctx, checkpoint, ActionReconciliation{})
}

// CreateWithReconciliation atomically records a checkpoint, explicit predecessor
// dispositions, remaining carry-forward transitions, and (for nonterminal
// checkpoints only) the new next action. A completed checkpoint is terminal and
// does not manufacture an unresolved action from its completion-state prose.
func (s Service) CreateWithReconciliation(ctx context.Context, checkpoint Checkpoint, reconciliation ActionReconciliation) (CreateResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CreateResult{}, err
	}
	defer tx.Rollback()
	result, err := CreateWithReconciliationTx(ctx, tx, checkpoint, reconciliation)
	if err != nil {
		return CreateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

// CreateWithReconciliationTx records a checkpoint and its action transitions in
// an existing transaction. The caller owns commit or rollback. This is reserved
// for higher-level operations that must couple checkpoint creation to another
// Continuity record without weakening checkpoint validation.
func CreateWithReconciliationTx(ctx context.Context, tx *sql.Tx, checkpoint Checkpoint, reconciliation ActionReconciliation) (CreateResult, error) {
	reconciliation, err := prepareActionReconciliation(reconciliation)
	if err != nil {
		return CreateResult{}, err
	}
	checkpoint, err = Prepare(checkpoint)
	if err != nil {
		return CreateResult{}, err
	}
	requestHash := ""
	if checkpoint.RequestKey != "" {
		requestHash, err = checkpointRequestHash(checkpoint, reconciliation)
		if err != nil {
			return CreateResult{}, err
		}
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return CreateResult{}, err
	}
	if checkpoint.RequestKey != "" {
		replayed, found, err := replayCheckpointRequest(ctx, tx, checkpoint.ProjectID, checkpoint.RequestKey, requestHash)
		if err != nil {
			return CreateResult{}, err
		}
		if found {
			return replayed, nil
		}
	}
	if err := validateLineage(ctx, tx, checkpoint); err != nil {
		return CreateResult{}, err
	}
	projection, err := planActions(ctx, tx, checkpoint.ProjectID, checkpoint.TaskID, checkpoint.Status, reconciliation)
	if err != nil {
		return CreateResult{}, err
	}
	// Read the task tip before this checkpoint becomes it.
	basedOn, err := basedOnCheckpoint(ctx, tx, checkpoint)
	if err != nil {
		return CreateResult{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO checkpoints(id, project_id, session_id, task_id, predecessor_checkpoint_id, request_key, request_hash, task_title, status, summary, next_action, payload_json, created_at) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
		checkpoint.ID, checkpoint.ProjectID, checkpoint.SessionID, checkpoint.TaskID, checkpoint.PredecessorCheckpointID, checkpoint.RequestKey, requestHash, checkpoint.Task, checkpoint.Status,
		checkpoint.Summary, checkpoint.NextAction, string(payload), checkpoint.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return CreateResult{}, err
	}
	if err := recordContext(ctx, tx, checkpoint, basedOn); err != nil {
		return CreateResult{}, err
	}
	var stamp *lease.Stamp
	if checkpoint.TaskID != "" {
		value, err := checkpoint.Fence.Stamp(ctx, tx, checkpoint.ProjectID, lease.StampCheckpoint, checkpoint.ID, "task", checkpoint.TaskID, checkpoint.CreatedAt)
		if err != nil {
			return CreateResult{}, err
		}
		stamp = &value
	}
	for i := range projection.Reconciled {
		update := reconciliationUpdate(reconciliation.Updates, projection.Reconciled[i].ID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_action_transitions(id,action_id,checkpoint_id,status,reason,created_at) VALUES (?,?,?,?,NULLIF(?,''),?)`,
			activity.NewID(), update.ID, checkpoint.ID, update.Status, update.Reason, checkpoint.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return CreateResult{}, err
		}
	}
	carried := projection.Remaining
	for i := range carried {
		carried[i].Status, carried[i].CarriedToCheckpoint = "carried_forward", checkpoint.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_action_transitions(id,action_id,checkpoint_id,status,created_at) VALUES (?,?,?,'carried_forward',?)`, activity.NewID(), carried[i].ID, checkpoint.ID, checkpoint.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return CreateResult{}, err
		}
	}
	if projection.CreatesNextAction {
		actionID := activity.NewID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_actions(id,project_id,source_checkpoint_id,description,created_at) VALUES (?,?,?,?,?)`, actionID, checkpoint.ProjectID, checkpoint.ID, checkpoint.NextAction, checkpoint.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return CreateResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkpoint_action_transitions(id,action_id,checkpoint_id,status,created_at) VALUES (?,?,?,'open',?)`, activity.NewID(), actionID, checkpoint.ID, checkpoint.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return CreateResult{}, err
		}
	}
	return CreateResult{Checkpoint: checkpoint, Lease: stamp, CarriedForward: carried, Reconciled: projection.Reconciled, ActionCreated: projection.CreatesNextAction}, nil
}

// ReplayRequest returns the original result for a matching idempotent request.
// A reused key with different content is rejected rather than silently aliased.
func (s Service) ReplayRequest(ctx context.Context, candidate Checkpoint, reconciliation ActionReconciliation) (*CreateResult, error) {
	if strings.TrimSpace(candidate.RequestKey) == "" {
		return nil, nil
	}
	var err error
	reconciliation, err = prepareActionReconciliation(reconciliation)
	if err != nil {
		return nil, err
	}
	candidate, err = Prepare(candidate)
	if err != nil {
		return nil, err
	}
	hash, err := checkpointRequestHash(candidate, reconciliation)
	if err != nil {
		return nil, err
	}
	result, found, err := replayCheckpointRequest(ctx, s.DB, candidate.ProjectID, candidate.RequestKey, hash)
	if err != nil || !found {
		return nil, err
	}
	return &result, nil
}

func checkpointRequestHash(candidate Checkpoint, reconciliation ActionReconciliation) (string, error) {
	candidate.ID = ""
	candidate.CreatedAt = time.Time{}
	body, err := json.Marshal(struct {
		Checkpoint     Checkpoint           `json:"checkpoint"`
		Reconciliation ActionReconciliation `json:"reconciliation"`
	}{candidate, reconciliation})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func replayCheckpointRequest(ctx context.Context, db interface {
	rowQueryer
	queryer
}, projectID, requestKey, requestHash string) (CreateResult, bool, error) {
	var checkpointID, storedHash, payload string
	err := db.QueryRowContext(ctx, `SELECT id,request_hash,payload_json FROM checkpoints WHERE project_id=? AND request_key=?`, projectID, requestKey).Scan(&checkpointID, &storedHash, &payload)
	if err == sql.ErrNoRows {
		return CreateResult{}, false, nil
	}
	if err != nil {
		return CreateResult{}, false, err
	}
	if storedHash != requestHash {
		return CreateResult{}, false, fmt.Errorf("checkpoint request key %q was already used with different content", requestKey)
	}
	var result CreateResult
	if err := json.Unmarshal([]byte(payload), &result.Checkpoint); err != nil {
		return CreateResult{}, false, err
	}
	result.Checkpoint.RequestKey = requestKey
	rows, err := db.QueryContext(ctx, `SELECT a.id,a.source_checkpoint_id,a.description,t.status,a.created_at
		FROM checkpoint_action_transitions t JOIN checkpoint_actions a ON a.id=t.action_id
		WHERE t.checkpoint_id=? AND t.status IN ('carried_forward','completed','superseded')
		ORDER BY a.created_at,a.id`, checkpointID)
	if err != nil {
		return CreateResult{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var action Action
		var created string
		if err := rows.Scan(&action.ID, &action.SourceCheckpointID, &action.Description, &action.Status, &created); err != nil {
			return CreateResult{}, false, err
		}
		action.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return CreateResult{}, false, err
		}
		if action.Status == "carried_forward" {
			action.CarriedToCheckpoint = checkpointID
			result.CarriedForward = append(result.CarriedForward, action)
		} else {
			result.Reconciled = append(result.Reconciled, action)
		}
	}
	if err := rows.Err(); err != nil {
		return CreateResult{}, false, err
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoint_actions WHERE source_checkpoint_id=?)`, checkpointID).Scan(&result.ActionCreated); err != nil {
		return CreateResult{}, false, err
	}
	return result, true, nil
}

func (s Service) ProjectActions(ctx context.Context, projectID, taskID, status, predecessorCheckpointID string, reconciliation ActionReconciliation) (ActionProjection, error) {
	var err error
	reconciliation, err = prepareActionReconciliation(reconciliation)
	if err != nil {
		return ActionProjection{}, err
	}
	return planActions(ctx, s.DB, projectID, strings.TrimSpace(taskID), strings.TrimSpace(status), reconciliation)
}

// ValidateLineage checks the opt-in strict continuation contract without writing.
// Callers that omit predecessor_checkpoint_id retain the legacy append behavior.
func (s Service) ValidateLineage(ctx context.Context, candidate Checkpoint) error {
	return validateLineage(ctx, s.DB, candidate)
}

func validateLineage(ctx context.Context, db rowQueryer, candidate Checkpoint) error {
	predecessorID := strings.TrimSpace(candidate.PredecessorCheckpointID)
	taskID := strings.TrimSpace(candidate.TaskID)
	// Every checkpoint names its task, and every checkpoint after a task's first
	// names that task's current tip. Parallel agents therefore cannot share an
	// untasked chain or silently append to another agent's task.
	if taskID == "" {
		return fmt.Errorf("checkpoint requires a task_id; choose a new task ID for new work")
	}
	state, err := taskState(ctx, db, candidate.ProjectID, taskID)
	if err != nil {
		return err
	}
	if state.Managed && state.Status != "active" {
		return fmt.Errorf("task %q is %s and cannot accept checkpoints", taskID, state.Status)
	}
	tip, err := currentTaskTip(ctx, db, candidate.ProjectID, taskID)
	if err != nil {
		return err
	}
	if reviewed := strings.TrimSpace(candidate.ReviewsTaskID); reviewed != "" {
		if reviewed == taskID {
			return fmt.Errorf("task %q cannot review itself", taskID)
		}
		reviewedTip, err := currentTaskTip(ctx, db, candidate.ProjectID, reviewed)
		if err != nil {
			return err
		}
		if reviewedTip == "" {
			return fmt.Errorf("reviewed task %q has no checkpoints in this project", reviewed)
		}
	}
	if tip == "" {
		if predecessorID != "" {
			return fmt.Errorf("new task %q has no checkpoint predecessor", taskID)
		}
		return nil
	}
	if predecessorID == "" {
		return fmt.Errorf("task %q already has checkpoints; continue it with predecessor checkpoint %q (its current tip), or start a new task ID", taskID, tip)
	}
	if predecessorID == strings.TrimSpace(candidate.ID) {
		return fmt.Errorf("checkpoint cannot continue itself")
	}
	var predecessorTaskID string
	err = db.QueryRowContext(ctx, `SELECT COALESCE(task_id,'') FROM checkpoints WHERE project_id=? AND id=?`, candidate.ProjectID, predecessorID).Scan(&predecessorTaskID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("predecessor checkpoint %q not found in this project", predecessorID)
	}
	if err != nil {
		return err
	}
	if predecessorTaskID != taskID {
		return fmt.Errorf("predecessor checkpoint %q belongs to task %q, not %q", predecessorID, predecessorTaskID, taskID)
	}
	if tip != predecessorID {
		return fmt.Errorf("predecessor checkpoint %q is stale; current task tip is %q", predecessorID, tip)
	}
	return nil
}

func prepareActionReconciliation(input ActionReconciliation) (ActionReconciliation, error) {
	if len(input.Updates) > 64 || len(input.CarryForwardActionIDs) > 64 {
		return ActionReconciliation{}, fmt.Errorf("checkpoint action reconciliation lists may contain at most 64 entries")
	}
	for i := range input.Updates {
		input.Updates[i].ID = strings.TrimSpace(input.Updates[i].ID)
		input.Updates[i].Status = strings.TrimSpace(input.Updates[i].Status)
		input.Updates[i].Reason = redact.String(strings.TrimSpace(input.Updates[i].Reason))
	}
	for i := range input.CarryForwardActionIDs {
		input.CarryForwardActionIDs[i] = strings.TrimSpace(input.CarryForwardActionIDs[i])
	}
	body, err := json.Marshal(input)
	if err != nil {
		return ActionReconciliation{}, fmt.Errorf("encode checkpoint action reconciliation: %w", err)
	}
	if len(body) > MaxBytes {
		return ActionReconciliation{}, fmt.Errorf("checkpoint action reconciliation exceeds 32 KiB limit")
	}
	return input, nil
}

// planActions projects a checkpoint's effect on its own task's unresolved
// actions. Other tasks' actions are invisible here, so parallel tasks never
// carry forward, reconcile, or absorb each other's actions.
func planActions(ctx context.Context, db queryer, projectID, taskID, status string, reconciliation ActionReconciliation) (ActionProjection, error) {
	current, err := unresolvedActions(ctx, db, projectID, actionScope{TaskID: taskID})
	if err != nil {
		return ActionProjection{}, err
	}
	byID := map[string]Action{}
	projection := ActionProjection{Current: []Action{}, Remaining: []Action{}, Reconciled: []Action{}, ExplicitlyCarried: []Action{}, CreatesNextAction: status != "completed"}
	for _, value := range current {
		byID[value.ID] = value
		projection.Current = append(projection.Current, value)
	}
	updates := map[string]ActionUpdate{}
	for _, update := range reconciliation.Updates {
		if update.ID == "" || update.Status != "completed" && update.Status != "superseded" {
			return ActionProjection{}, fmt.Errorf("action updates require an unresolved ID and completed or superseded status")
		}
		if _, duplicate := updates[update.ID]; duplicate {
			return ActionProjection{}, fmt.Errorf("checkpoint action %q is updated more than once", update.ID)
		}
		if _, exists := byID[update.ID]; !exists {
			return ActionProjection{}, actionNotInTask(ctx, db, projectID, update.ID, taskID)
		}
		updates[update.ID] = update
	}
	carried := map[string]bool{}
	for _, id := range reconciliation.CarryForwardActionIDs {
		if id == "" || carried[id] {
			return ActionProjection{}, fmt.Errorf("carry-forward action IDs must be nonblank and unique")
		}
		if _, exists := byID[id]; !exists {
			return ActionProjection{}, actionNotInTask(ctx, db, projectID, id, taskID)
		}
		if _, resolved := updates[id]; resolved {
			return ActionProjection{}, fmt.Errorf("checkpoint action %q cannot be both reconciled and carried forward", id)
		}
		carried[id] = true
		projection.ExplicitlyCarried = append(projection.ExplicitlyCarried, byID[id])
	}
	missing := []string{}
	for _, value := range current {
		if update, resolved := updates[value.ID]; resolved {
			action := value
			action.Status = update.Status
			projection.Reconciled = append(projection.Reconciled, action)
			continue
		}
		projection.Remaining = append(projection.Remaining, value)
		if status == "completed" && !carried[value.ID] {
			missing = append(missing, value.ID)
		}
	}
	if len(missing) > 0 {
		detail := strings.Join(missing, ", ")
		if len(missing) > 10 {
			detail = strings.Join(missing[:10], ", ") + fmt.Sprintf(" (and %d more)", len(missing)-10)
		}
		return ActionProjection{}, fmt.Errorf("completed checkpoint must reconcile or explicitly carry forward same-task actions: %s", detail)
	}
	projection.ProjectedUnresolvedCount = len(projection.Remaining)
	if projection.CreatesNextAction {
		projection.ProjectedUnresolvedCount++
	}
	return projection, nil
}

func reconciliationUpdate(updates []ActionUpdate, id string) ActionUpdate {
	for _, update := range updates {
		if strings.TrimSpace(update.ID) == id {
			return ActionUpdate{ID: id, Status: strings.TrimSpace(update.Status), Reason: strings.TrimSpace(update.Reason)}
		}
	}
	return ActionUpdate{}
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// actionScope selects unresolved actions: one task's (TaskID, where "" means
// untasked checkpoints) unless All, optionally leaving out the actions created
// by one checkpoint.
type actionScope struct {
	TaskID              string
	All                 bool
	ExcludeCheckpointID string
}

func unresolvedActions(ctx context.Context, db queryer, projectID string, scope actionScope) ([]Action, error) {
	rows, err := db.QueryContext(ctx, `WITH latest AS (
		SELECT t.action_id, t.status, t.checkpoint_id, ROW_NUMBER() OVER (PARTITION BY t.action_id ORDER BY t.created_at DESC, t.id DESC) AS n
		FROM checkpoint_action_transitions t JOIN checkpoint_actions a ON a.id=t.action_id WHERE a.project_id=?
	) SELECT a.id,a.source_checkpoint_id,a.description,l.status,COALESCE(l.checkpoint_id,''),a.created_at,COALESCE(c.task_id,'')
	FROM checkpoint_actions a JOIN latest l ON l.action_id=a.id AND l.n=1 JOIN checkpoints c ON c.id=a.source_checkpoint_id
	WHERE a.project_id=? AND l.status IN ('open','carried_forward') AND a.source_checkpoint_id<>?
		AND (? OR COALESCE(c.task_id,'')=?)
	ORDER BY a.created_at,a.id`, projectID, projectID, scope.ExcludeCheckpointID, scope.All, strings.TrimSpace(scope.TaskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Action{}
	for rows.Next() {
		var a Action
		var created string
		if err := rows.Scan(&a.ID, &a.SourceCheckpointID, &a.Description, &a.Status, &a.CarriedToCheckpoint, &created, &a.TaskID); err != nil {
			return nil, err
		}
		a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// actionNotInTask explains why an action ID is not among taskID's unresolved
// actions, naming its owning task when it belongs to another one.
func actionNotInTask(ctx context.Context, db queryer, projectID, actionID, taskID string) error {
	all, err := unresolvedActions(ctx, db, projectID, actionScope{All: true})
	if err != nil {
		return err
	}
	for _, action := range all {
		if action.ID == actionID {
			return fmt.Errorf("checkpoint action %q belongs to task %q, not %q; a checkpoint reconciles only its own task's actions", actionID, action.TaskID, taskID)
		}
	}
	return fmt.Errorf("checkpoint action %q is not unresolved in this project", actionID)
}

// Unresolved lists one task's unresolved actions ("" selects untasked
// checkpoints), leaving out those created by excludeCheckpointID.
func (s Service) Unresolved(ctx context.Context, projectID, taskID, excludeCheckpointID string) ([]Action, error) {
	return unresolvedActions(ctx, s.DB, projectID, actionScope{TaskID: taskID, ExcludeCheckpointID: excludeCheckpointID})
}

// OtherTaskActionCount counts unresolved actions owned by tasks other than
// taskID ("" is the untasked group). Retired task lineages are not active
// reminders; their original action history remains available through ListActions.
func (s Service) OtherTaskActionCount(ctx context.Context, projectID, taskID string) (int, error) {
	actions, err := unresolvedActions(ctx, s.DB, projectID, actionScope{All: true})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, action := range actions {
		if action.TaskID != strings.TrimSpace(taskID) {
			closed, err := TaskClosed(ctx, s.DB, projectID, action.TaskID)
			if err != nil {
				return 0, err
			}
			if closed {
				continue
			}
			count++
		}
	}
	return count, nil
}

// ListActions lists unresolved actions for one task, or with all for every task.
// Without either, it lists the project's actions only when they belong to a
// single task, so a caller cannot mistake other tasks' actions for its own.
func (s Service) ListActions(ctx context.Context, projectID, taskID string, all bool) ([]Action, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID != "" && all {
		return nil, fmt.Errorf("select either a task or all tasks, not both")
	}
	if taskID != "" {
		return s.Unresolved(ctx, projectID, taskID, "")
	}
	actions, err := unresolvedActions(ctx, s.DB, projectID, actionScope{All: true})
	if err != nil || all {
		return actions, err
	}
	counts := map[string]int{}
	var order []string
	for _, action := range actions {
		if counts[action.TaskID] == 0 {
			order = append(order, action.TaskID)
		}
		counts[action.TaskID]++
	}
	if len(order) <= 1 {
		return actions, nil
	}
	sort.Strings(order)
	parts := make([]string, len(order))
	for i, task := range order {
		label := task
		if label == "" {
			label = "(untasked)"
		}
		parts[i] = fmt.Sprintf("%s: %d", label, counts[task])
	}
	return nil, fmt.Errorf("unresolved actions belong to several tasks (%s); select your task, or all tasks to list every one", strings.Join(parts, ", "))
}

func (s Service) PendingTexts(ctx context.Context, projectID string) ([]string, error) {
	actions, err := unresolvedActions(ctx, s.DB, projectID, actionScope{All: true})
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(actions))
	for i, a := range actions {
		texts[i] = a.Description
	}
	return texts, nil
}

// TransitionAction completes or supersedes one action. A nonblank taskID must
// name the action's own task, which guards against closing another agent's work.
func (s Service) TransitionAction(ctx context.Context, projectID, taskID, actionID, status, reason string) (Action, error) {
	actionID, status, reason, taskID = strings.TrimSpace(actionID), strings.TrimSpace(status), strings.TrimSpace(reason), strings.TrimSpace(taskID)
	if projectID == "" || actionID == "" {
		return Action{}, fmt.Errorf("project and action ID are required")
	}
	if status != "completed" && status != "superseded" {
		return Action{}, fmt.Errorf("action status must be completed or superseded")
	}
	var action Action
	var created string
	err := s.DB.QueryRowContext(ctx, `SELECT a.id,a.source_checkpoint_id,a.description,a.created_at,COALESCE(c.task_id,'') FROM checkpoint_actions a JOIN checkpoints c ON c.id=a.source_checkpoint_id WHERE a.project_id=? AND a.id=?`, projectID, actionID).Scan(&action.ID, &action.SourceCheckpointID, &action.Description, &created, &action.TaskID)
	if err == sql.ErrNoRows {
		return Action{}, fmt.Errorf("action %q not found in this project", actionID)
	}
	if err != nil {
		return Action{}, err
	}
	if taskID != "" && action.TaskID != taskID {
		return Action{}, fmt.Errorf("action %q belongs to task %q, not %q", actionID, action.TaskID, taskID)
	}
	action.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Action{}, err
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO checkpoint_action_transitions(id,action_id,status,reason,created_at)
		SELECT ?,?,?,?,? WHERE (SELECT status FROM checkpoint_action_transitions WHERE action_id=? ORDER BY created_at DESC,id DESC LIMIT 1) IN ('open','carried_forward')`,
		activity.NewID(), actionID, status, reason, time.Now().UTC().Format(time.RFC3339Nano), actionID)
	if err != nil {
		return Action{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Action{}, err
	}
	if changed != 1 {
		return Action{}, fmt.Errorf("action is already completed or superseded")
	}
	action.Status = status
	return action, nil
}

func (s Service) Latest(ctx context.Context, projectID string) (*Checkpoint, error) {
	return s.LatestSelected(ctx, projectID, Selection{})
}

func (s Service) LatestSelected(ctx context.Context, projectID string, selection Selection) (*Checkpoint, error) {
	selection.CheckpointID = strings.TrimSpace(selection.CheckpointID)
	selection.TaskID = strings.TrimSpace(selection.TaskID)
	selection.SessionID = strings.TrimSpace(selection.SessionID)
	if projectID == "" {
		return nil, fmt.Errorf("project is required")
	}
	count := 0
	for _, value := range []string{selection.CheckpointID, selection.TaskID, selection.SessionID} {
		if value != "" {
			count++
		}
	}
	if count > 1 {
		return nil, fmt.Errorf("select at most one of checkpoint ID, task ID or session ID")
	}
	query := `SELECT payload_json FROM checkpoints WHERE project_id = ?`
	args := []any{projectID}
	switch {
	case selection.CheckpointID != "":
		query += ` AND id = ?`
		args = append(args, selection.CheckpointID)
	case selection.TaskID != "":
		query += ` AND task_id = ?`
		args = append(args, selection.TaskID)
	case selection.SessionID != "":
		query += ` AND session_id = ?`
		args = append(args, selection.SessionID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT 1`
	var payload string
	err := s.DB.QueryRowContext(ctx, query, args...).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal([]byte(payload), &checkpoint); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func (s Service) Find(ctx context.Context, projectID string, request FindRequest) (FindResult, error) {
	request.Query = strings.TrimSpace(request.Query)
	if projectID == "" || request.Query == "" {
		return FindResult{}, fmt.Errorf("project and non-blank query are required")
	}
	if len(request.Query) > 256 {
		return FindResult{}, fmt.Errorf("query exceeds 256 byte limit")
	}
	if request.Limit == 0 {
		request.Limit = DefaultHistoryLimit
	}
	if request.Limit < 1 || request.Limit > MaxHistoryLimit {
		return FindResult{}, fmt.Errorf("limit must be between 1 and %d", MaxHistoryLimit)
	}
	cursorTime, cursorID, err := decodeCursor(request.Cursor)
	if err != nil {
		return FindResult{}, err
	}
	literal := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(request.Query)
	pattern := "%" + literal + "%"
	rows, err := s.DB.QueryContext(ctx, `SELECT id, task_title, status, summary, next_action, payload_json, created_at
		FROM checkpoints
		WHERE project_id = ? AND (task_title LIKE ? ESCAPE '\' COLLATE NOCASE OR summary LIKE ? ESCAPE '\' COLLATE NOCASE OR next_action LIKE ? ESCAPE '\' COLLATE NOCASE OR payload_json LIKE ? ESCAPE '\' COLLATE NOCASE)
		AND (? = '' OR created_at < ? OR (created_at = ? AND id < ?))
		ORDER BY created_at DESC, id DESC LIMIT ?`, projectID, pattern, pattern, pattern, pattern,
		cursorTime, cursorTime, cursorTime, cursorID, request.Limit+1)
	if err != nil {
		return FindResult{}, err
	}
	defer rows.Close()
	result := FindResult{SchemaVersion: 1, Hits: []HistoryHit{}, Authority: "caller_asserted", Disclaimer: "Matches are deterministic lexical reads of caller-supplied checkpoint fields; no model ranked or interpreted them, and inspection does not resume or authorize work."}
	for rows.Next() {
		var hit HistoryHit
		var summary, nextAction, payload, created string
		if err := rows.Scan(&hit.ID, &hit.Task, &hit.Status, &summary, &nextAction, &payload, &created); err != nil {
			return FindResult{}, err
		}
		if err := json.Unmarshal([]byte(payload), new(Checkpoint)); err != nil {
			return FindResult{}, fmt.Errorf("decode checkpoint %s: %w", hit.ID, err)
		}
		hit.Authority = "caller_asserted"
		hit.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return FindResult{}, fmt.Errorf("decode checkpoint time %s: %w", hit.ID, err)
		}
		hit.Excerpt = historyExcerpt(summary, nextAction)
		result.Hits = append(result.Hits, hit)
	}
	if err := rows.Err(); err != nil {
		return FindResult{}, err
	}
	if len(result.Hits) > request.Limit {
		last := result.Hits[request.Limit-1]
		result.NextCursor = encodeCursor(last.CreatedAt.Format(time.RFC3339Nano), last.ID)
		result.Hits = result.Hits[:request.Limit]
	}
	return result, nil
}

func (s Service) Get(ctx context.Context, projectID, id string, maxBytes int) (*Export, error) {
	id = strings.TrimSpace(id)
	if projectID == "" || id == "" {
		return nil, fmt.Errorf("project and checkpoint ID are required")
	}
	if maxBytes == 0 {
		maxBytes = DefaultExportBytes
	}
	if maxBytes < 512 || maxBytes > MaxBytes {
		return nil, fmt.Errorf("max bytes must be between 512 and %d", MaxBytes)
	}
	var payload string
	err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM checkpoints WHERE project_id = ? AND id = ?`, projectID, id).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value Checkpoint
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return nil, fmt.Errorf("decode checkpoint %s: %w", id, err)
	}
	markdown, truncated := renderMarkdown(value, maxBytes)
	return &Export{SchemaVersion: 1, ID: value.ID, CreatedAt: value.CreatedAt, Markdown: markdown, MaxBytes: maxBytes, Truncated: truncated, Authority: "caller_asserted", Disclaimer: "This is a bounded export of a saved caller-supplied handoff. Inspecting or copying it does not make it current, resume it, or authorize its next action."}, nil
}

func decodeCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	parts := strings.SplitN(string(body), "\n", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	return parts[0], parts[1], nil
}

func encodeCursor(createdAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\n" + id))
}

func historyExcerpt(summary, nextAction string) string {
	text := strings.TrimSpace(summary)
	if text == "" {
		text = strings.TrimSpace(nextAction)
	}
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > 240 {
		return string(runes[:239]) + "…"
	}
	return string(runes)
}

func renderMarkdown(value Checkpoint, maxBytes int) (string, bool) {
	var out strings.Builder
	appendText := func(text string) bool {
		if out.Len()+len(text) > maxBytes {
			return false
		}
		out.WriteString(text)
		return true
	}
	appendText("# Historical handoff\n\n")
	appendText(fmt.Sprintf("- ID: `%s`\n- Created: %s\n- Status: %s\n- Authority: caller_asserted\n\n", value.ID, value.CreatedAt.Format(time.RFC3339Nano), value.Status))
	sections := []struct {
		heading string
		items   []string
	}{
		{"Task", []string{value.Task}}, {"Summary", []string{value.Summary}}, {"Next action", []string{value.NextAction}},
		{"Completed", value.Completed}, {"Changes", value.Changes}, {"Verification claims (not independently verified)", value.Verified}, {"Evidence references (caller-supplied)", value.EvidenceRefs},
	}
	truncated := false
	for _, section := range sections {
		if len(section.items) == 0 || (len(section.items) == 1 && strings.TrimSpace(section.items[0]) == "") {
			continue
		}
		if !appendText("## " + section.heading + "\n\n") {
			truncated = true
			break
		}
		for _, item := range section.items {
			prefix := "- "
			if len(section.items) == 1 && (section.heading == "Task" || section.heading == "Summary" || section.heading == "Next action") {
				prefix = ""
			}
			if !appendText(prefix + item + "\n") {
				truncated = true
				break
			}
		}
		if truncated || !appendText("\n") {
			truncated = true
			break
		}
	}
	if !truncated && len(value.Attempts) > 0 {
		if !appendText("## Prior attempts\n\n") {
			truncated = true
		} else {
			for _, attempt := range value.Attempts {
				line := fmt.Sprintf("- %s — %s", attempt.Description, attempt.Outcome)
				if attempt.Reason != "" {
					line += ": " + attempt.Reason
				}
				if !appendText(line + "\n") {
					truncated = true
					break
				}
			}
		}
	}
	if truncated {
		marker := "\n[Export truncated at the requested byte budget.]\n"
		current := out.String()
		for len(current)+len(marker) > maxBytes && current != "" {
			runes := []rune(current)
			current = string(runes[:len(runes)-1])
		}
		return strings.TrimRight(current, "\n") + marker, true
	}
	return out.String(), false
}
