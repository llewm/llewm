package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

const (
	reviewersListed   = 3
	reviewSummaryMax  = 160
	reviewSummaryTail = "…"
)

// reviewNotice describes a task's review links: the task it reviews (with that
// task's current tip), and the latest checkpoint of each task that reviews it.
// It returns a full line and a names-only fallback.
func reviewNotice(ctx context.Context, db *sql.DB, projectID string, latest *checkpoint.Checkpoint) (string, string) {
	if latest == nil || latest.TaskID == "" {
		return "", ""
	}
	var full, short []string
	if latest.ReviewsTaskID != "" {
		tip, err := (checkpoint.Service{DB: db}).LatestSelected(ctx, projectID, checkpoint.Selection{TaskID: latest.ReviewsTaskID})
		if err == nil && tip != nil {
			full = append(full, fmt.Sprintf("this task reviews `%s` (%s; tip %s)", latest.ReviewsTaskID, tip.Status, tip.ID))
			short = append(short, fmt.Sprintf("this task reviews `%s`", latest.ReviewsTaskID))
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT task_id, id, status, summary FROM checkpoints
		WHERE project_id=? AND json_extract(payload_json,'$.reviews_task_id')=? AND task_id IS NOT NULL
		ORDER BY created_at DESC, id DESC`, projectID, latest.TaskID)
	if err == nil {
		seen := map[string]bool{}
		var reviewers, names []string
		for rows.Next() {
			var taskID, id, status, summary string
			if rows.Scan(&taskID, &id, &status, &summary) != nil || seen[taskID] {
				continue
			}
			seen[taskID] = true
			if len(reviewers) == reviewersListed {
				continue
			}
			if len(summary) > reviewSummaryMax {
				summary = strings.ToValidUTF8(summary[:reviewSummaryMax], "") + reviewSummaryTail
			}
			reviewers = append(reviewers, fmt.Sprintf("`%s` (%s, checkpoint %s): %s", taskID, status, id, summary))
			names = append(names, "`"+taskID+"`")
		}
		rows.Close()
		if len(reviewers) > 0 {
			more := ""
			if len(seen) > len(reviewers) {
				more = fmt.Sprintf(" and %d more", len(seen)-len(reviewers))
			}
			full = append(full, "reviewed by "+strings.Join(reviewers, "; ")+more+". Read a reviewer's checkpoint before continuing; its findings may change this task's plan")
			short = append(short, "reviewed by "+strings.Join(names, ", ")+more+"; read their checkpoints before continuing")
		}
	}
	if len(full) == 0 {
		return "", ""
	}
	return strings.Join(full, "; ") + ".", strings.Join(short, "; ") + "."
}
