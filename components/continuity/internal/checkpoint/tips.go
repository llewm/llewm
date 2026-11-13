package checkpoint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// TaskTip is the newest checkpoint of one task, with the branches it was
// written on when they were recorded.
type TaskTip struct {
	TaskID        string         `json:"task_id"`
	CheckpointID  string         `json:"checkpoint_id"`
	Status        string         `json:"status"`
	CreatedAt     time.Time      `json:"created_at"`
	Branches      []CodeBranch   `json:"branches,omitempty"`
	RestartResume *RestartResume `json:"restart_resume,omitempty"`
}

// OnBranch reports whether the tip was written with member on branch.
func (t TaskTip) OnBranch(member, branch string) bool {
	for _, value := range t.Branches {
		if value.Member == member && value.Branch != "" && value.Branch == branch {
			return true
		}
	}
	return false
}

// TaskTips returns each task's newest checkpoint, newest first, the same order
// and tie-break as LatestSelected. Checkpoints without a task are left out.
func TaskTips(ctx context.Context, db queryer, projectID string) ([]TaskTip, error) {
	rows, err := db.QueryContext(ctx, `SELECT task_id, id, status, created_at, branches_json, payload_json FROM (
			SELECT c.task_id, c.id, c.status, c.created_at, c.payload_json, COALESCE(b.branches_json, '') AS branches_json,
				ROW_NUMBER() OVER (PARTITION BY c.task_id ORDER BY c.created_at DESC, c.id DESC) AS rank
			FROM checkpoints c LEFT JOIN checkpoint_branches b ON b.project_id = c.project_id AND b.checkpoint_id = c.id
			WHERE c.project_id = ? AND c.task_id IS NOT NULL AND c.task_id <> '')
		WHERE rank = 1 ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tips []TaskTip
	for rows.Next() {
		var tip TaskTip
		var created, branches, payload string
		if err := rows.Scan(&tip.TaskID, &tip.CheckpointID, &tip.Status, &created, &branches, &payload); err != nil {
			return nil, err
		}
		var saved Checkpoint
		if err := json.Unmarshal([]byte(payload), &saved); err != nil {
			return nil, fmt.Errorf("decode checkpoint %s: %w", tip.CheckpointID, err)
		}
		tip.RestartResume = saved.RestartResume
		if tip.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("decode checkpoint time %s: %w", tip.CheckpointID, err)
		}
		if branches != "" {
			if err := json.Unmarshal([]byte(branches), &tip.Branches); err != nil {
				return nil, fmt.Errorf("decode checkpoint branches %s: %w", tip.CheckpointID, err)
			}
		}
		tips = append(tips, tip)
	}
	return tips, rows.Err()
}

// TaskClosed reports whether a task's lineage ended (rekeyed, superseded, or
// abandoned), so it is no longer active whatever its last checkpoint says.
func TaskClosed(ctx context.Context, db rowQueryer, projectID, taskID string) (bool, error) {
	state, err := taskState(ctx, db, projectID, taskID)
	if err != nil {
		return false, err
	}
	return state.TerminalEventID != "", nil
}

// TaskFinished reports whether a task's newest checkpoint is completed or its
// lineage ended. A finished task is no longer anyone's current work, so new
// activity must not be credited to it. A task with no checkpoint is unfinished.
func TaskFinished(ctx context.Context, db rowQueryer, projectID, taskID string) (bool, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM checkpoints WHERE project_id=? AND task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, projectID, taskID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status == "completed" {
		return true, nil
	}
	return TaskClosed(ctx, db, projectID, taskID)
}

// TaskFinishedAt reports whether a task was finished at a past instant: its
// newest checkpoint at or before then was completed, or its lineage had ended.
// Times are compared parsed, since stored RFC 3339 text does not sort by time.
func TaskFinishedAt(ctx context.Context, db queryer, projectID, taskID string, at time.Time) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,status,created_at FROM checkpoints WHERE project_id=? AND task_id=?`, projectID, taskID)
	if err != nil {
		return false, err
	}
	var tipID, tipStatus string
	var tipAt time.Time
	for rows.Next() {
		var id, status, created string
		if err := rows.Scan(&id, &status, &created); err != nil {
			rows.Close()
			return false, err
		}
		createdAt, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			rows.Close()
			return false, fmt.Errorf("decode checkpoint time %s: %w", id, err)
		}
		if createdAt.After(at) {
			continue
		}
		if tipID == "" || createdAt.After(tipAt) || (createdAt.Equal(tipAt) && id > tipID) {
			tipID, tipStatus, tipAt = id, status, createdAt
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if tipStatus == "completed" {
		return true, nil
	}
	ended, err := db.QueryContext(ctx, `SELECT id,created_at FROM task_lineage_events WHERE project_id=? AND task_id=? AND operation IN ('rekey','supersede','abandon')`, projectID, taskID)
	if err != nil {
		return false, err
	}
	defer ended.Close()
	for ended.Next() {
		var id, created string
		if err := ended.Scan(&id, &created); err != nil {
			return false, err
		}
		createdAt, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return false, fmt.Errorf("decode lineage event time %s: %w", id, err)
		}
		if !createdAt.After(at) {
			return true, nil
		}
	}
	return false, ended.Err()
}

// SessionCheckpoint is one task checkpoint a session saved.
type SessionCheckpoint struct {
	ID, TaskID string
	CreatedAt  time.Time
}

// SessionCheckpoints lists a session's task checkpoints oldest first, ordered
// by parsed time then ID.
func SessionCheckpoints(ctx context.Context, db queryer, projectID, sessionID string) ([]SessionCheckpoint, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,task_id,created_at FROM checkpoints WHERE project_id=? AND session_id=? AND task_id IS NOT NULL AND task_id<>''`, projectID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionCheckpoint
	for rows.Next() {
		var value SessionCheckpoint
		var created string
		if err := rows.Scan(&value.ID, &value.TaskID, &created); err != nil {
			return nil, err
		}
		if value.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("decode checkpoint time %s: %w", value.ID, err)
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result, rows.Err()
}
