package authorship

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

type execQueryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// attributionsSQL is every unrevoked commit_authors observation with its
// current task: the newest retask amendment's, else the observed one, with
// certainty 'inferred' once a retask applies. Readers select from it as a
// subquery; it is not a view because History snapshot reuse rejects views
// other than its reviewed ones.
const attributionsSQL = `SELECT a.project_id, a.member, a.commit_sha, a.session_id, a.client, a.tool_name, a.observed_at,
	COALESCE(r.task_id, COALESCE(a.task_id, '')) AS task_id,
	CASE WHEN r.id IS NULL THEN a.certainty ELSE 'inferred' END AS certainty
FROM commit_authors a
LEFT JOIN commit_author_amendments r ON r.id = (
	SELECT m.id FROM commit_author_amendments m
	WHERE m.project_id = a.project_id AND m.member = a.member AND m.commit_sha = a.commit_sha
		AND m.session_id = a.session_id AND m.kind = 'retask'
	ORDER BY m.id DESC LIMIT 1)
WHERE NOT EXISTS (
	SELECT 1 FROM commit_author_amendments v
	WHERE v.project_id = a.project_id AND v.member = a.member AND v.commit_sha = a.commit_sha
		AND v.session_id = a.session_id AND v.kind = 'revoke')`

// attributionsFrom is a FROM clause over attributionsSQL.
const attributionsFrom = ` FROM (` + attributionsSQL + `) `

// attribution is one row of attributionsSQL.
type attribution struct {
	member, commit, sessionID, taskID string
	observedAt                        time.Time
}

func attributions(ctx context.Context, db execQueryer, query string, args ...any) ([]attribution, error) {
	rows, err := db.QueryContext(ctx, `SELECT member,commit_sha,session_id,task_id,observed_at`+attributionsFrom+`WHERE `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []attribution
	for rows.Next() {
		var value attribution
		var observed string
		if err := rows.Scan(&value.member, &value.commit, &value.sessionID, &value.taskID, &observed); err != nil {
			return nil, err
		}
		if value.observedAt, err = time.Parse(time.RFC3339Nano, observed); err != nil {
			return nil, fmt.Errorf("commit_authors %s observed_at %q: %w", value.commit, observed, err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func retask(ctx context.Context, db execQueryer, projectID string, value attribution, taskID, reason string, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO commit_author_amendments(project_id,member,commit_sha,session_id,kind,task_id,reason,amended_at) VALUES (?,?,?,?,'retask',?,?,?)`,
		projectID, value.member, value.commit, value.sessionID, taskID, reason, now.UTC().Format(time.RFC3339Nano))
	return err
}

// RetaskSession names taskID as the inferred task of the session's commits that
// have none and were observed after since (any time when zero) and no later
// than until. A checkpoint calls it with the session's previous checkpoint and
// itself: a commit made before a task's first checkpoint, or after the
// session finished its last task, belongs to the task the session checkpoints
// next. Observations stay unchanged; each match gets a retask amendment.
func RetaskSession(ctx context.Context, db execQueryer, projectID, sessionID, taskID string, since, until, now time.Time) (int, error) {
	if sessionID == "" || taskID == "" {
		return 0, nil
	}
	values, err := attributions(ctx, db, `project_id=? AND session_id=? AND task_id=''`, projectID, sessionID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, value := range values {
		if (!since.IsZero() && !value.observedAt.After(since)) || value.observedAt.After(until) {
			continue
		}
		if err := retask(ctx, db, projectID, value, taskID, "first checkpoint of the session after the commit names this task", now); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// RetaskFinished repairs attributions observed before cutoff, by builds that
// credited a session's finished task or recorded no task for a commit made
// before the session's first checkpoint. A commit crediting a task already
// finished (completed or ended) when it was observed, or none, gets the task
// the session checkpointed next, which RetaskSession now does at each
// checkpoint. A finished task the session reopened next keeps its commit; a
// finished task the session never followed with a checkpoint gives way to no
// task, and a task-less commit with no later checkpoint stays task-less.
func RetaskFinished(ctx context.Context, db *sql.DB, projectID string, cutoff, now time.Time) (int, error) {
	values, err := attributions(ctx, db, `project_id=?`, projectID)
	if err != nil {
		return 0, err
	}
	type move struct {
		value          attribution
		taskID, reason string
	}
	var moves []move
	sessions := map[string][]checkpoint.SessionCheckpoint{}
	for _, value := range values {
		if !value.observedAt.Before(cutoff) {
			continue
		}
		reason := "the commit had no task"
		if value.taskID != "" {
			finished, err := checkpoint.TaskFinishedAt(ctx, db, projectID, value.taskID, value.observedAt)
			if err != nil {
				return 0, err
			}
			if !finished {
				continue
			}
			reason = "task " + value.taskID + " was finished when the commit was observed"
		}
		saved, ok := sessions[value.sessionID]
		if !ok {
			if saved, err = checkpoint.SessionCheckpoints(ctx, db, projectID, value.sessionID); err != nil {
				return 0, err
			}
			sessions[value.sessionID] = saved
		}
		next, why := "", reason+", and the session saved no later checkpoint"
		for _, item := range saved {
			if item.CreatedAt.After(value.observedAt) {
				next, why = item.TaskID, reason+"; the session's next checkpoint names this task"
				break
			}
		}
		if next != value.taskID {
			moves = append(moves, move{value, next, why})
		}
	}
	if len(moves) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, item := range moves {
		if err := retask(ctx, tx, projectID, item.value, item.taskID, item.reason, now); err != nil {
			return 0, err
		}
	}
	return len(moves), tx.Commit()
}
