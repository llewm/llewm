package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
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
