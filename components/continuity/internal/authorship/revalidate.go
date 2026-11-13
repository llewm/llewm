package authorship

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// RevalidationWindow bounds how long before an observation the commit-creating
// reflog entry may be. A hook observes at the end of the tool call that made the
// commit, so a genuine attribution is seconds to minutes after creation; a later
// observation saw history some other session, a person, or another host created.
const RevalidationWindow = 15 * time.Minute

// revalidationSkew tolerates reflog timestamps truncated to whole seconds.
const revalidationSkew = 2 * time.Second

// Revalidation is one observation and whether it keeps its attribution under
// commit-creating reflog evidence.
type Revalidation struct {
	Member     string
	Commit     string
	SessionID  string
	ObservedAt time.Time
	Keep       bool
	Reason     string
}

// Checkout is one repository whose attributions are revalidated.
type Checkout struct {
	Member, Root string
}

// RevalidatePending runs the one-time repair that migration 097 requests for a
// project: it revalidates the attributions observed before the migration's
// cutoff in every checkout, then clears the request. Builds before commit
// 922c22f recorded attributions without creation evidence. On any error the
// request stays for the next hook to retry; revocations are idempotent.
//
// Remove once every device has migrated past 097; the revocation table and the
// readers' filter stay.
func RevalidatePending(ctx context.Context, db *sql.DB, projectID string, checkouts []Checkout, now time.Time) ([]Revalidation, error) {
	var cutoffText string
	err := db.QueryRowContext(ctx, `SELECT cutoff FROM commit_author_revalidations_pending WHERE project_id=?`, projectID).Scan(&cutoffText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff, err := time.Parse(time.RFC3339Nano, cutoffText)
	if err != nil {
		return nil, fmt.Errorf("revalidation cutoff %q: %w", cutoffText, err)
	}
	var all []Revalidation
	for _, checkout := range checkouts {
		items, err := revalidate(ctx, db, projectID, checkout, cutoff, now)
		if err != nil {
			return all, err
		}
		all = append(all, items...)
	}
	_, err = db.ExecContext(ctx, `DELETE FROM commit_author_revalidations_pending WHERE project_id=?`, projectID)
	return all, err
}

// revalidate checks each unrevoked observation made before cutoff against
// commit-creating entries in all of the checkout's reflogs (branches and every
// worktree HEAD), keeping it only when such an entry falls within
// RevalidationWindow before it, and appends a revocation for each failure.
// Observation rows are never changed. Expired reflog history is no evidence.
func revalidate(ctx context.Context, db *sql.DB, projectID string, checkout Checkout, cutoff, now time.Time) ([]Revalidation, error) {
	created, err := creationTimes(ctx, checkout.Root)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT a.commit_sha,a.session_id,a.observed_at FROM commit_authors a
		WHERE a.project_id=? AND a.member=? AND NOT EXISTS (`+revokedMatch+`) ORDER BY a.observed_at,a.commit_sha`, projectID, checkout.Member)
	if err != nil {
		return nil, err
	}
	var result []Revalidation
	for rows.Next() {
		item := Revalidation{Member: checkout.Member}
		var observed string
		if err := rows.Scan(&item.Commit, &item.SessionID, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		item.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("commit_authors %s observed_at %q: %w", item.Commit, observed, err)
		}
		if !item.ObservedAt.Before(cutoff) {
			continue
		}
		item.Keep, item.Reason = judge(created[item.Commit], item.ObservedAt)
		result = append(result, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
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
			projectID, checkout.Member, item.Commit, item.SessionID, item.Reason, at); err != nil {
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
		if withinWindow(t, observed) {
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

// withinWindow reports whether a creation at created can be what an
// observation at observed saw a tool call make.
func withinWindow(created, observed time.Time) bool {
	return !created.After(observed.Add(revalidationSkew)) && observed.Sub(created) <= RevalidationWindow
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
		if at, ok := reflogTime(fields[1]); ok {
			result[fields[0]] = append(result[fields[0]], at)
		}
	}
	return result, nil
}

// reflogTime parses the time from a reflog selector printed with --date=unix,
// such as HEAD@{1762900000}.
func reflogTime(selector string) (time.Time, bool) {
	open, end := strings.LastIndex(selector, "@{"), strings.LastIndex(selector, "}")
	if open < 0 || end < open {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(selector[open+2:end], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

// revokedMatch is the correlated subquery matching a revocation of the
// commit_authors row aliased a.
const revokedMatch = `SELECT 1 FROM commit_author_revocations r WHERE r.project_id=a.project_id AND r.member=a.member AND r.commit_sha=a.commit_sha AND r.session_id=a.session_id`
