package authorship

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// RevalidationWindow bounds how long before an observation the commit-creating
// reflog entry may be. A hook observes at the end of the tool call that made the
// commit, so a genuine attribution is seconds to minutes after creation; a later
// observation saw history some other session or host created.
const RevalidationWindow = 15 * time.Minute

// revalidationSkew tolerates reflog timestamps truncated to whole seconds.
const revalidationSkew = 2 * time.Second

// Revalidation is one unrevoked observation and whether it keeps its
// attribution under commit-creating reflog evidence.
type Revalidation struct {
	Member     string    `json:"member,omitempty"`
	Commit     string    `json:"commit"`
	SessionID  string    `json:"session_id"`
	TaskID     string    `json:"task_id,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Keep       bool      `json:"keep"`
	Reason     string    `json:"reason"`
}

// Revalidate checks every unrevoked commit_authors observation for one checkout
// against commit-creating entries in all of its reflogs (branches and every
// worktree HEAD). An observation keeps its attribution only when such an entry
// for its commit falls within RevalidationWindow before it. With apply, it
// appends a revocation for each failing observation; observation rows are never
// changed. Expired or missing reflog history is no evidence, so it revokes.
func Revalidate(ctx context.Context, db *sql.DB, projectID, member, root string, apply bool, now time.Time) ([]Revalidation, error) {
	created, err := creationTimes(ctx, root)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT a.commit_sha,a.session_id,COALESCE(a.task_id,''),a.observed_at FROM commit_authors a
		WHERE a.project_id=? AND a.member=? AND NOT EXISTS (`+revokedMatch+`) ORDER BY a.observed_at,a.commit_sha`, projectID, member)
	if err != nil {
		return nil, err
	}
	var result []Revalidation
	for rows.Next() {
		var item Revalidation
		var observed string
		if err := rows.Scan(&item.Commit, &item.SessionID, &item.TaskID, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		item.Member = member
		item.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("commit_authors %s observed_at %q: %w", item.Commit, observed, err)
		}
		item.Keep, item.Reason = judge(created[item.Commit], item.ObservedAt)
		result = append(result, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if !apply {
		return result, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	at := now.UTC().Format(time.RFC3339Nano)
	for _, item := range result {
		if item.Keep {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO commit_author_revocations(project_id,member,commit_sha,session_id,reason,revoked_at) VALUES (?,?,?,?,?,?)`,
			projectID, member, item.Commit, item.SessionID, item.Reason, at); err != nil {
			return nil, err
		}
	}
	return result, tx.Commit()
}

func judge(times []time.Time, observed time.Time) (bool, string) {
	if len(times) == 0 {
		return false, "no commit-creating reflog entry for this commit; incoming history or expired reflog"
	}
	nearest := times[0]
	for _, t := range times {
		if !t.After(observed.Add(revalidationSkew)) && observed.Sub(t) <= RevalidationWindow {
			return true, "commit-creating reflog entry " + observed.Sub(t).Round(time.Second).String() + " before observation"
		}
		if t.Sub(observed).Abs() < nearest.Sub(observed).Abs() {
			nearest = t
		}
	}
	gap := observed.Sub(nearest).Round(time.Second)
	if gap < 0 {
		return false, "commit-creating reflog entry " + (-gap).String() + " after observation"
	}
	return false, "commit-creating reflog entry " + gap.String() + " before observation, outside " + RevalidationWindow.String()
}

// creationTimes maps each commit to the times of commit-creating entries in any
// reflog of the checkout's repository, including other worktrees' HEADs.
func creationTimes(ctx context.Context, root string) (map[string][]time.Time, error) {
	output, err := gitenv.Command(ctx, "-C", root, "log", "-g", "--all", "--date=unix", "--format=%H%x00%gd%x00%gs").Output()
	if err != nil {
		return nil, fmt.Errorf("read reflogs: %w", err)
	}
	result := map[string][]time.Time{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.SplitN(line, "\x00", 3)
		if len(fields) != 3 || !createsCommit(fields[2]) {
			continue
		}
		selector := fields[1]
		open, end := strings.LastIndex(selector, "@{"), strings.LastIndex(selector, "}")
		if open < 0 || end < open {
			continue
		}
		seconds, err := strconv.ParseInt(selector[open+2:end], 10, 64)
		if err != nil {
			continue
		}
		result[fields[0]] = append(result[fields[0]], time.Unix(seconds, 0).UTC())
	}
	return result, nil
}

// revokedMatch is the correlated subquery matching a revocation of the
// commit_authors row aliased a.
const revokedMatch = `SELECT 1 FROM commit_author_revocations r WHERE r.project_id=a.project_id AND r.member=a.member AND r.commit_sha=a.commit_sha AND r.session_id=a.session_id`
