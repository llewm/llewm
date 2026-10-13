package checkpoint

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ListRequest selects metadata only, without loading checkpoint bodies.
type ListRequest struct {
	TaskID string `json:"task_id,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type Summary struct {
	ID                      string    `json:"id"`
	TaskID                  string    `json:"task_id,omitempty"`
	PredecessorCheckpointID string    `json:"predecessor_checkpoint_id,omitempty"`
	Task                    string    `json:"task"`
	Status                  string    `json:"status"`
	CreatedAt               time.Time `json:"created_at"`
	Authority               string    `json:"authority"`
}

type ListPage struct {
	SchemaVersion int       `json:"schema_version"`
	Checkpoints   []Summary `json:"checkpoints"`
	NextCursor    string    `json:"next_cursor,omitempty"`
	Authority     string    `json:"authority"`
	Disclaimer    string    `json:"disclaimer"`
}

// List is a bounded read of all checkpoints or one task's checkpoint chain.
// The cursor orders by recorded time and identity; it is not a frozen snapshot.
func (s Service) List(ctx context.Context, projectID string, request ListRequest) (ListPage, error) {
	request.TaskID = strings.TrimSpace(request.TaskID)
	if projectID == "" || len(request.TaskID) > 200 {
		return ListPage{}, fmt.Errorf("project is required and task ID must not exceed 200 bytes")
	}
	if request.Limit == 0 {
		request.Limit = DefaultHistoryLimit
	}
	if request.Limit < 1 || request.Limit > MaxHistoryLimit {
		return ListPage{}, fmt.Errorf("limit must be between 1 and %d", MaxHistoryLimit)
	}
	at, id, err := decodeCursor(request.Cursor)
	if err != nil {
		return ListPage{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,COALESCE(task_id,''),COALESCE(predecessor_checkpoint_id,''),task_title,status,created_at FROM checkpoints WHERE project_id=? AND (?='' OR task_id=?) AND (?='' OR created_at<? OR (created_at=? AND id<?)) ORDER BY created_at DESC,id DESC LIMIT ?`, projectID, request.TaskID, request.TaskID, at, at, at, id, request.Limit+1)
	if err != nil {
		return ListPage{}, err
	}
	defer rows.Close()
	result := ListPage{SchemaVersion: 1, Checkpoints: []Summary{}, Authority: "mixed", Disclaimer: "Checkpoint titles and status are caller asserted; identities, lineage and ordering are machine observed. Inspection does not resume work or change selection. Cursors are not frozen snapshots."}
	for rows.Next() {
		var summary Summary
		var created string
		if err := rows.Scan(&summary.ID, &summary.TaskID, &summary.PredecessorCheckpointID, &summary.Task, &summary.Status, &created); err != nil {
			return ListPage{}, err
		}
		summary.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return ListPage{}, err
		}
		summary.Authority = "caller_asserted"
		result.Checkpoints = append(result.Checkpoints, summary)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, err
	}
	if len(result.Checkpoints) > request.Limit {
		last := result.Checkpoints[request.Limit-1]
		result.NextCursor = encodeCursor(last.CreatedAt.Format(time.RFC3339Nano), last.ID)
		result.Checkpoints = result.Checkpoints[:request.Limit]
	}
	return result, nil
}
