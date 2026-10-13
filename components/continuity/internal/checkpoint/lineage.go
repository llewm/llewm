package checkpoint

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	DefaultLineageReportLimit = 50
	MaxLineageReportLimit     = 100
)

type TaskTransition struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Operation    string    `json:"operation"`
	TaskID       string    `json:"task_id"`
	TargetTaskID string    `json:"target_task_id,omitempty"`
	CheckpointID string    `json:"checkpoint_id,omitempty"`
	RequestKey   string    `json:"request_key"`
	Reason       string    `json:"reason,omitempty"`
	Authority    string    `json:"authority"`
	CreatedAt    time.Time `json:"created_at"`
}

type TaskState struct {
	Managed         bool   `json:"managed"`
	Status          string `json:"status"`
	OriginOperation string `json:"origin_operation,omitempty"`
	TerminalEventID string `json:"terminal_event_id,omitempty"`
	ReplacementTask string `json:"replacement_task_id,omitempty"`
}

type LineageAnomaly struct {
	TaskID               string   `json:"task_id"`
	Codes                []string `json:"codes"`
	CheckpointCount      int      `json:"checkpoint_count"`
	RootCount            int      `json:"root_count"`
	TipCount             int      `json:"tip_count"`
	SelectedCheckpointID string   `json:"selected_checkpoint_id,omitempty"`
	SelectedStatus       string   `json:"selected_status,omitempty"`
	LifecycleStatus      string   `json:"lifecycle_status"`
	ReplacementTaskID    string   `json:"replacement_task_id,omitempty"`
}

type LineageReport struct {
	SchemaVersion int              `json:"schema_version"`
	Anomalies     []LineageAnomaly `json:"anomalies"`
	Truncated     bool             `json:"truncated"`
	Limit         int              `json:"limit"`
	Authority     string           `json:"authority"`
	Disclaimer    string           `json:"disclaimer"`
}

type TaskLineageView struct {
	TaskID     string           `json:"task_id"`
	State      TaskState        `json:"state"`
	CurrentTip string           `json:"current_tip,omitempty"`
	Events     []TaskTransition `json:"events"`
	Truncated  bool             `json:"truncated"`
	Limit      int              `json:"limit"`
	Authority  string           `json:"authority"`
	Disclaimer string           `json:"disclaimer"`
}

// TransitionTask appends one explicit task lifecycle event. Request keys are
// required and replay only the byte-equivalent normalized request.
func (s Service) TransitionTask(ctx context.Context, input TaskTransition) (TaskTransition, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Operation = strings.TrimSpace(input.Operation)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.TargetTaskID = strings.TrimSpace(input.TargetTaskID)
	input.CheckpointID = strings.TrimSpace(input.CheckpointID)
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.Reason = redact.String(strings.TrimSpace(input.Reason))
	if input.ProjectID == "" || input.TaskID == "" || input.RequestKey == "" {
		return TaskTransition{}, fmt.Errorf("project, task ID and request key are required")
	}
	if len(input.TaskID) > 200 || len(input.TargetTaskID) > 200 || len(input.RequestKey) > 200 {
		return TaskTransition{}, fmt.Errorf("task IDs and request key must not exceed 200 bytes")
	}
	if !oneOfLineage(input.Operation, "start", "fork", "rekey", "supersede", "abandon") {
		return TaskTransition{}, fmt.Errorf("task operation must be start, fork, rekey, supersede or abandon")
	}
	wantsTarget := input.Operation == "fork" || input.Operation == "rekey" || input.Operation == "supersede"
	if wantsTarget != (input.TargetTaskID != "") {
		return TaskTransition{}, fmt.Errorf("%s operation requires exactly one target task ID", input.Operation)
	}
	if input.TargetTaskID == input.TaskID {
		return TaskTransition{}, fmt.Errorf("task operation cannot target the same task")
	}
	hash, err := taskTransitionHash(input)
	if err != nil {
		return TaskTransition{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TaskTransition{}, err
	}
	defer tx.Rollback()
	var existingHash string
	var existing TaskTransition
	var created string
	err = tx.QueryRowContext(ctx, `SELECT id,operation,task_id,COALESCE(target_task_id,''),COALESCE(checkpoint_id,''),request_key,request_hash,COALESCE(reason,''),created_at
		FROM task_lineage_events WHERE project_id=? AND request_key=?`, input.ProjectID, input.RequestKey).
		Scan(&existing.ID, &existing.Operation, &existing.TaskID, &existing.TargetTaskID, &existing.CheckpointID, &existing.RequestKey, &existingHash, &existing.Reason, &created)
	if err == nil {
		if existingHash != hash {
			return TaskTransition{}, fmt.Errorf("task request key %q was already used with different content", input.RequestKey)
		}
		existing.ProjectID, existing.Authority = input.ProjectID, "caller_asserted"
		existing.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		return existing, err
	}
	if err != sql.ErrNoRows {
		return TaskTransition{}, err
	}

	sourceState, err := taskState(ctx, tx, input.ProjectID, input.TaskID)
	if err != nil {
		return TaskTransition{}, err
	}
	sourceTip, err := currentTaskTip(ctx, tx, input.ProjectID, input.TaskID)
	if err != nil {
		return TaskTransition{}, err
	}
	switch input.Operation {
	case "start":
		if sourceState.Managed {
			return TaskTransition{}, fmt.Errorf("task %q already has an explicit lifecycle", input.TaskID)
		}
		if err := requireExactTip(input.CheckpointID, sourceTip, true); err != nil {
			return TaskTransition{}, fmt.Errorf("start task %q: %w", input.TaskID, err)
		}
	case "fork", "rekey":
		if !sourceState.Managed || sourceState.Status != "active" {
			return TaskTransition{}, fmt.Errorf("source task %q is not explicitly active", input.TaskID)
		}
		if sourceTip == "" {
			return TaskTransition{}, fmt.Errorf("source task %q has no checkpoint tip", input.TaskID)
		}
		if err := requireExactTip(input.CheckpointID, sourceTip, false); err != nil {
			return TaskTransition{}, fmt.Errorf("%s task %q: %w", input.Operation, input.TaskID, err)
		}
		if err := requireUnusedTask(ctx, tx, input.ProjectID, input.TargetTaskID); err != nil {
			return TaskTransition{}, err
		}
	case "supersede":
		if !sourceState.Managed || sourceState.Status != "active" {
			return TaskTransition{}, fmt.Errorf("task %q is not explicitly active", input.TaskID)
		}
		targetState, err := taskState(ctx, tx, input.ProjectID, input.TargetTaskID)
		if err != nil {
			return TaskTransition{}, err
		}
		if !targetState.Managed || targetState.Status != "active" {
			return TaskTransition{}, fmt.Errorf("replacement task %q is not explicitly active", input.TargetTaskID)
		}
		if err := requireExactTip(input.CheckpointID, sourceTip, true); err != nil {
			return TaskTransition{}, fmt.Errorf("supersede task %q: %w", input.TaskID, err)
		}
	case "abandon":
		if !sourceState.Managed || sourceState.Status != "active" {
			return TaskTransition{}, fmt.Errorf("task %q is not explicitly active", input.TaskID)
		}
		if err := requireExactTip(input.CheckpointID, sourceTip, true); err != nil {
			return TaskTransition{}, fmt.Errorf("abandon task %q: %w", input.TaskID, err)
		}
	}

	input.ID = activity.NewID()
	input.Authority = "caller_asserted"
	input.CreatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO task_lineage_events(id,project_id,operation,task_id,target_task_id,checkpoint_id,request_key,request_hash,reason,created_at)
		VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,NULLIF(?,''),?)`, input.ID, input.ProjectID, input.Operation, input.TaskID, input.TargetTaskID, input.CheckpointID, input.RequestKey, hash, input.Reason, input.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return TaskTransition{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskTransition{}, err
	}
	return input, nil
}

func taskTransitionHash(input TaskTransition) (string, error) {
	input.ID, input.ProjectID, input.Authority = "", "", ""
	input.CreatedAt = time.Time{}
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func requireExactTip(got, tip string, allowEmpty bool) error {
	if tip == "" && allowEmpty && got == "" {
		return nil
	}
	if got == "" {
		return fmt.Errorf("current checkpoint tip %q is required", tip)
	}
	if got != tip {
		return fmt.Errorf("checkpoint %q is stale; current tip is %q", got, tip)
	}
	return nil
}

func requireUnusedTask(ctx context.Context, db rowQueryer, projectID, taskID string) error {
	state, err := taskState(ctx, db, projectID, taskID)
	if err != nil {
		return err
	}
	var checkpoints int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints WHERE project_id=? AND task_id=?`, projectID, taskID).Scan(&checkpoints); err != nil {
		return err
	}
	if state.Managed || checkpoints != 0 {
		return fmt.Errorf("target task %q is already in use", taskID)
	}
	return nil
}

func currentTaskTip(ctx context.Context, db rowQueryer, projectID, taskID string) (string, error) {
	var tip string
	err := db.QueryRowContext(ctx, `SELECT id FROM checkpoints WHERE project_id=? AND task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, projectID, taskID).Scan(&tip)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tip, err
}

func taskState(ctx context.Context, db rowQueryer, projectID, taskID string) (TaskState, error) {
	var result TaskState
	err := db.QueryRowContext(ctx, `SELECT operation FROM task_lineage_events WHERE project_id=? AND ((operation='start' AND task_id=?) OR (operation IN ('fork','rekey') AND target_task_id=?)) ORDER BY created_at,id LIMIT 1`, projectID, taskID, taskID).Scan(&result.OriginOperation)
	if err == sql.ErrNoRows {
		return TaskState{Status: "legacy_unmanaged"}, nil
	}
	if err != nil {
		return TaskState{}, err
	}
	result.Managed, result.Status = true, "active"
	var operation string
	err = db.QueryRowContext(ctx, `SELECT id,operation,COALESCE(target_task_id,'') FROM task_lineage_events WHERE project_id=? AND task_id=? AND operation IN ('rekey','supersede','abandon') ORDER BY created_at,id LIMIT 1`, projectID, taskID).Scan(&result.TerminalEventID, &operation, &result.ReplacementTask)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil {
		return TaskState{}, err
	}
	result.Status = terminalLifecycleStatus(operation)
	return result, nil
}

// TaskLineage returns bounded append-only lifecycle history and derived current
// state for one task without changing checkpoint selection.
func (s Service) TaskLineage(ctx context.Context, projectID, taskID string, limit int) (TaskLineageView, error) {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return TaskLineageView{}, fmt.Errorf("project and task ID are required")
	}
	if limit == 0 {
		limit = DefaultLineageReportLimit
	}
	if limit < 1 || limit > MaxLineageReportLimit {
		return TaskLineageView{}, fmt.Errorf("limit must be between 1 and %d", MaxLineageReportLimit)
	}
	state, err := taskState(ctx, s.DB, projectID, taskID)
	if err != nil {
		return TaskLineageView{}, err
	}
	tip, err := currentTaskTip(ctx, s.DB, projectID, taskID)
	if err != nil {
		return TaskLineageView{}, err
	}
	result := TaskLineageView{TaskID: taskID, State: state, CurrentTip: tip, Events: []TaskTransition{}, Limit: limit, Authority: "mixed", Disclaimer: "Lifecycle event meaning and reasons are caller asserted; ordering, current checkpoint tip, and derived structural state are machine observed. This read does not change selection or task state."}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,operation,task_id,COALESCE(target_task_id,''),COALESCE(checkpoint_id,''),request_key,COALESCE(reason,''),created_at
		FROM task_lineage_events WHERE project_id=? AND (task_id=? OR target_task_id=?) ORDER BY created_at,id LIMIT ?`, projectID, taskID, taskID, limit+1)
	if err != nil {
		return TaskLineageView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var event TaskTransition
		var created string
		if err := rows.Scan(&event.ID, &event.Operation, &event.TaskID, &event.TargetTaskID, &event.CheckpointID, &event.RequestKey, &event.Reason, &created); err != nil {
			return TaskLineageView{}, err
		}
		if len(result.Events) == limit {
			result.Truncated = true
			break
		}
		event.ProjectID, event.Authority = projectID, "caller_asserted"
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return TaskLineageView{}, err
		}
		result.Events = append(result.Events, event)
	}
	return result, rows.Err()
}

// LineageAnomalies reports deterministic structural risks without repairing or
// reselecting any checkpoint.
func (s Service) LineageAnomalies(ctx context.Context, projectID string, limit int) (LineageReport, error) {
	if limit == 0 {
		limit = DefaultLineageReportLimit
	}
	if limit < 1 || limit > MaxLineageReportLimit {
		return LineageReport{}, fmt.Errorf("limit must be between 1 and %d", MaxLineageReportLimit)
	}
	result := LineageReport{SchemaVersion: 1, Anomalies: []LineageAnomaly{}, Limit: limit, Authority: "machine_observed", Disclaimer: "This deterministic report identifies checkpoint graph and task-lifecycle anomalies; it does not infer intent, repair lineage, or change checkpoint selection."}
	rows, err := s.DB.QueryContext(ctx, `WITH task_ids(task_id) AS (
		SELECT task_id FROM checkpoints WHERE project_id=? AND task_id IS NOT NULL
		UNION SELECT task_id FROM task_lineage_events WHERE project_id=?
		UNION SELECT target_task_id FROM task_lineage_events WHERE project_id=? AND target_task_id IS NOT NULL
	), stats AS (
		SELECT t.task_id,
		 (SELECT COUNT(*) FROM checkpoints c WHERE c.project_id=? AND c.task_id=t.task_id) checkpoint_count,
		 (SELECT COUNT(*) FROM checkpoints c WHERE c.project_id=? AND c.task_id=t.task_id AND c.predecessor_checkpoint_id IS NULL) root_count,
		 (SELECT COUNT(*) FROM checkpoints c WHERE c.project_id=? AND c.task_id=t.task_id AND NOT EXISTS (SELECT 1 FROM checkpoints child WHERE child.project_id=c.project_id AND child.predecessor_checkpoint_id=c.id)) tip_count,
		 COALESCE((SELECT id FROM checkpoints c WHERE c.project_id=? AND c.task_id=t.task_id ORDER BY created_at DESC,id DESC LIMIT 1),'') selected_id,
		 COALESCE((SELECT status FROM checkpoints c WHERE c.project_id=? AND c.task_id=t.task_id ORDER BY created_at DESC,id DESC LIMIT 1),'') selected_status,
		 COALESCE((SELECT operation FROM task_lineage_events e WHERE e.project_id=? AND e.task_id=t.task_id AND e.operation IN ('rekey','supersede','abandon') ORDER BY created_at,id LIMIT 1),'') terminal_operation,
		 COALESCE((SELECT target_task_id FROM task_lineage_events e WHERE e.project_id=? AND e.task_id=t.task_id AND e.operation IN ('rekey','supersede') ORDER BY created_at,id LIMIT 1),'') replacement_task,
		 EXISTS(SELECT 1 FROM task_lineage_events e WHERE e.project_id=? AND ((e.operation='start' AND e.task_id=t.task_id) OR (e.operation IN ('fork','rekey') AND e.target_task_id=t.task_id))) managed
		FROM task_ids t
	) SELECT task_id,checkpoint_count,root_count,tip_count,selected_id,selected_status,terminal_operation,replacement_task,managed
	FROM stats WHERE (managed AND (root_count>1 OR tip_count>1)) OR (terminal_operation<>'' AND checkpoint_count>0) OR (managed AND terminal_operation='' AND selected_status='completed')
	ORDER BY task_id LIMIT ?`, projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID, projectID, limit+1)
	if err != nil {
		return LineageReport{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var value LineageAnomaly
		var terminal string
		var managed bool
		if err := rows.Scan(&value.TaskID, &value.CheckpointCount, &value.RootCount, &value.TipCount, &value.SelectedCheckpointID, &value.SelectedStatus, &terminal, &value.ReplacementTaskID, &managed); err != nil {
			return LineageReport{}, err
		}
		value.LifecycleStatus = "legacy_unmanaged"
		if managed {
			value.LifecycleStatus = "active"
		}
		if value.RootCount > 1 {
			value.Codes = append(value.Codes, "orphan")
		}
		if value.TipCount > 1 {
			value.Codes = append(value.Codes, "multiple_tip")
		}
		if terminal != "" {
			value.LifecycleStatus = terminalLifecycleStatus(terminal)
			value.Codes = append(value.Codes, "terminal_selection")
		} else if managed && value.SelectedStatus == "completed" {
			value.Codes = append(value.Codes, "terminal_selection")
		}
		if len(result.Anomalies) == limit {
			result.Truncated = true
			break
		}
		result.Anomalies = append(result.Anomalies, value)
	}
	return result, rows.Err()
}

func oneOfLineage(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func terminalLifecycleStatus(operation string) string {
	switch operation {
	case "rekey":
		return "rekeyed"
	case "supersede":
		return "superseded"
	case "abandon":
		return "abandoned"
	default:
		return operation
	}
}
