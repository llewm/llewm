package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StaleWriterError reports a process whose writer API is below the store's
// floor: a write-incompatible build has opened the store since it started.
type StaleWriterError struct {
	Process, Floor int
}

func (e *StaleWriterError) Error() string {
	return fmt.Sprintf("this Continuity process runs writer API %d, but the store requires writer API %d; restart the client so a compatible server handles writes", e.Process, e.Floor)
}

// WriterFloor returns the store's writer floor, or 0 when none was recorded.
// The physical column retains its original mcp_api name so already-running
// servers can keep checking the floor across the compatibility correction.
func WriterFloor(ctx context.Context, db *sql.DB) (int, error) {
	var floor int
	err := db.QueryRowContext(ctx, `SELECT mcp_api FROM writer_floor WHERE singleton=1`).Scan(&floor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return floor, err
}

// RaiseWriterFloor raises the floor to api; it never lowers it.
func RaiseWriterFloor(ctx context.Context, db *sql.DB, api int) error {
	_, err := db.ExecContext(ctx, `INSERT INTO writer_floor(singleton,mcp_api,raised_at) VALUES (1,?,?)
		ON CONFLICT(singleton) DO UPDATE SET mcp_api=excluded.mcp_api, raised_at=excluded.raised_at WHERE excluded.mcp_api > writer_floor.mcp_api`,
		api, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// SetWriterFloor sets the floor exactly, for a deliberate rollback to an older
// build. Ordinary opens only raise it.
func SetWriterFloor(ctx context.Context, db *sql.DB, api int) error {
	if api <= 0 {
		return fmt.Errorf("writer floor must be positive")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO writer_floor(singleton,mcp_api,raised_at) VALUES (1,?,?)
		ON CONFLICT(singleton) DO UPDATE SET mcp_api=excluded.mcp_api, raised_at=excluded.raised_at`,
		api, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// CheckWriter refuses when a newer build raised the floor above api.
func CheckWriter(ctx context.Context, db *sql.DB, api int) error {
	floor, err := WriterFloor(ctx, db)
	if err != nil {
		return err
	}
	if floor > api {
		return &StaleWriterError{Process: api, Floor: floor}
	}
	return nil
}
