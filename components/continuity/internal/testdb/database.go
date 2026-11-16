// Package testdb provides isolated current-schema databases for tests that are
// not testing schema initialization or migration. Each process builds one closed
// pristine image from the real migrations; each fixture gets its own file and
// connection pool. No live database, project data or cross-run cache is shared.
package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

var schemaImage = sync.OnceValues(buildImage)

// Create requires a new path and never resets or overwrites an existing store.
// Callers own the returned database and must close it before deleting its path.
// Migration/initialization tests must use database.Initialize instead.
func Create(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image, err := schemaImage()
	if err != nil {
		return nil, fmt.Errorf("prepare test schema: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := f.Write(image)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return nil, errors.Join(err, os.Remove(path))
	}
	db, err := database.Open(ctx, path)
	if err != nil {
		return nil, errors.Join(err, os.Remove(path))
	}
	// This is the only random seed in an empty, migrated current-schema store.
	// Project-local generations and portable identities are created later by the
	// real project/identity APIs, independently in each fixture.
	result, err := db.ExecContext(ctx, `UPDATE history_sync_generation SET token=lower(hex(randomblob(16))) WHERE project_id=''`)
	if err == nil {
		var count int64
		count, err = result.RowsAffected()
		if err == nil && count != 1 {
			err = fmt.Errorf("test schema has %d global generation rows, want 1", count)
		}
	}
	if err != nil {
		return nil, errors.Join(err, db.Close(), os.Remove(path))
	}
	return db, nil
}

func buildImage() (image []byte, err error) {
	dir, err := os.MkdirTemp("", "llewm-test-schema-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	path := filepath.Join(dir, "schema.db")
	db, err := database.Initialize(context.Background(), path)
	if err != nil {
		return nil, err
	}
	var busy, log, checkpointed int
	err = db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed)
	if err == nil && busy != 0 {
		err = fmt.Errorf("test schema checkpoint is busy")
	}
	if err := errors.Join(err, db.Close()); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
