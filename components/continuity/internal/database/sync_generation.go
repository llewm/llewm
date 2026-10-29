package database

import (
	"context"
	"database/sql"
)

// A proof covers both project-local changes and changes with global or unknown
// ownership. Missing generations fail closed rather than imply an empty history.
const syncGenerationQuery = `SELECT g.token||':'||p.token FROM history_sync_generation g JOIN history_sync_generation p ON p.project_id=? WHERE g.project_id=''`

func SyncGeneration(ctx context.Context, db *sql.DB, projectID string) (string, error) {
	var token string
	err := db.QueryRowContext(ctx, syncGenerationQuery, projectID).Scan(&token)
	return token, err
}

// RememberSyncProof compares generations and saves in one statement, so a
// concurrent committed write cannot be blessed by a stale completion proof.
func RememberSyncProof(ctx context.Context, db *sql.DB, projectID, route, generation, tree, archive string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO history_sync_proofs(project_id,route,generation,tree,archive)
	SELECT ?,?,?,?,? WHERE (`+syncGenerationQuery+`)=?
	ON CONFLICT(project_id,route) DO UPDATE SET generation=excluded.generation,tree=excluded.tree,archive=excluded.archive`,
		projectID, route, generation, tree, archive, projectID, generation)
	return err
}
