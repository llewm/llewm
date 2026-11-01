package database

import (
	"context"
	"database/sql"
	"errors"
)

// A proof covers both project-local changes and changes with global or unknown
// ownership. Missing generations fail closed rather than imply an empty history.
// Schema changes invalidate even a subsequently restored tracking definition:
// writes made while its trigger was absent cannot be recovered as a delta.
const syncGenerationQuery = `SELECT g.token||'-'||(SELECT schema_version FROM pragma_schema_version)||':'||p.token FROM history_sync_generation g JOIN history_sync_generation p ON p.project_id=? WHERE g.project_id=''`

func SyncGeneration(ctx context.Context, db *sql.DB, projectID string) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	known, err := HistoryCanonicalTracking(ctx, tx)
	if err != nil {
		return "", err
	}
	if !known {
		return "", ErrCanonicalTracking
	}
	var token string
	err = tx.QueryRowContext(ctx, syncGenerationQuery, projectID).Scan(&token)
	return token, err
}

// RememberSyncProof compares generations and saves in one statement, so a
// concurrent committed write cannot be blessed by a stale completion proof.
func RememberSyncProof(ctx context.Context, db *sql.DB, projectID, route, generation, tree, archive string) error {
	current, err := SyncGeneration(ctx, db, projectID)
	if errors.Is(err, ErrCanonicalTracking) || (err == nil && current != generation) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO history_sync_proofs(project_id,route,generation,tree,archive)
	SELECT ?,?,?,?,? WHERE (`+syncGenerationQuery+`)=?
	ON CONFLICT(project_id,route) DO UPDATE SET generation=excluded.generation,tree=excluded.tree,archive=excluded.archive`,
		projectID, route, generation, tree, archive, projectID, generation)
	return err
}
