package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SyncChange identifies a committed input, in ingestion order rather than its
// historical timestamp. Both sides of updates are retained by the journal.
type SyncChange struct {
	Sequence              int64
	Table, Key, Operation string
}

// SyncCursor is a receipt for a consumer's materialized state, not an import
// receipt or permission to discard journal entries.
type SyncCursor struct {
	Project, Consumer string
	Sequence          int64
	Generation, State string
}

// SyncSnapshot reads the journal position on the caller's existing transaction.
// Consumers taking a receipt after their own canonical writes must use the same
// writer transaction; a later independent snapshot could bless unrelated writes.
func SyncSnapshot(ctx context.Context, tx *sql.Tx, project, consumer string) (SyncCursor, error) {
	c := SyncCursor{Project: project, Consumer: consumer}
	if err := tx.QueryRowContext(ctx, syncGenerationQuery, project).Scan(&c.Generation); err != nil {
		return c, err
	}
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM history_sync_changes WHERE project_id=?`, project).Scan(&c.Sequence)
	return c, err
}

func (c SyncCursor) checksum() string {
	body, _ := json.Marshal(c)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

// ReadSyncChanges returns a bounded project delta and the snapshot to acknowledge.
// A nonempty fallback reason requires complete validation. Global changes include
// journal damage and unknown ownership; they can never be consumed as a delta.
func ReadSyncChanges(ctx context.Context, db *sql.DB, project, consumer, state string, limit int) (SyncCursor, []SyncChange, string, error) {
	next := SyncCursor{Project: project, Consumer: consumer}
	if project == "" || consumer == "" || limit < 1 || limit > 100000 {
		return next, nil, "", fmt.Errorf("invalid sync cursor request")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return next, nil, "", err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, syncGenerationQuery, project).Scan(&next.Generation); err != nil {
		return next, nil, "", err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM history_sync_changes WHERE project_id=?`, project).Scan(&next.Sequence); err != nil {
		return next, nil, "", err
	}
	saved := SyncCursor{Project: project, Consumer: consumer}
	var checksum string
	err = tx.QueryRowContext(ctx, `SELECT sequence,generation,state,checksum FROM history_sync_cursors WHERE project_id=? AND consumer=?`, project, consumer).Scan(&saved.Sequence, &saved.Generation, &saved.State, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return next, nil, "missing_cursor", nil
	}
	if err != nil {
		return next, nil, "", err
	}
	if saved.checksum() != checksum || saved.Sequence < 0 || saved.Sequence > next.Sequence {
		return next, nil, "damaged_cursor", nil
	}
	if saved.State != state {
		return next, nil, "materialization_changed", nil
	}
	global := func(g string) string { v, _, _ := strings.Cut(g, ":"); return v }
	if global(saved.Generation) != global(next.Generation) {
		return next, nil, "global_change", nil
	}
	if saved.Sequence == next.Sequence && saved.Generation != next.Generation {
		return next, nil, "missing_changes", nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,table_name,row_key,operation FROM history_sync_changes WHERE project_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, project, saved.Sequence, next.Sequence, limit+1)
	if err != nil {
		return next, nil, "", err
	}
	defer rows.Close()
	var changes []SyncChange
	for rows.Next() {
		var c SyncChange
		if err = rows.Scan(&c.Sequence, &c.Table, &c.Key, &c.Operation); err != nil {
			return next, nil, "", err
		}
		changes = append(changes, c)
	}
	if err = rows.Err(); err != nil {
		return next, nil, "", err
	}
	if len(changes) > limit {
		return next, nil, "delta_limit", nil
	}
	return next, changes, "", nil
}

// RememberSyncCursor acknowledges only this exact database snapshot. Filesystem
// consumers call it after durable materialization; failure merely causes replay.
// A racing write cannot be blessed by a later generation read.
func RememberSyncCursor(ctx context.Context, db *sql.DB, c SyncCursor, state string) (bool, error) {
	c.State = state
	result, err := db.ExecContext(ctx, `INSERT INTO history_sync_cursors(project_id,consumer,sequence,generation,state,checksum)
 SELECT ?,?,?,?,?,? WHERE (`+syncGenerationQuery+`)=? AND (SELECT COALESCE(MAX(sequence),0) FROM history_sync_changes WHERE project_id=?)=?
 ON CONFLICT(project_id,consumer) DO UPDATE SET sequence=excluded.sequence,generation=excluded.generation,state=excluded.state,checksum=excluded.checksum`, c.Project, c.Consumer, c.Sequence, c.Generation, c.State, c.checksum(), c.Project, c.Generation, c.Project, c.Sequence)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
