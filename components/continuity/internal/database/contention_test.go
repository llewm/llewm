package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

func contentionDatabase(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contention.db")
	first, err := openReadWrite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if _, err := first.Exec(`CREATE TABLE counter (value INTEGER); INSERT INTO counter VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	second, err := openReadWrite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	return first, second
}

// Reproduce the old export failure: a deferred reader cannot become a writer
// after another connection commits, even with busy_timeout configured.
func TestDeferredSnapshotCannotUpgradeAfterConcurrentCommit(t *testing.T) {
	first, second := contentionDatabase(t)
	ctx := context.Background()
	conn, err := first.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN DEFERRED`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	var value int
	if err := conn.QueryRowContext(ctx, `SELECT value FROM counter`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecContext(ctx, `UPDATE counter SET value=value+1`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `UPDATE counter SET value=value+1`)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 517 {
		t.Fatalf("stale snapshot upgrade = %v, want SQLITE_BUSY_SNAPSHOT (517)", err)
	}
}

func TestWriteTransactionReservesBeforeReading(t *testing.T) {
	first, second := contentionDatabase(t)
	ctx := context.Background()
	tx, err := first.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var value int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM counter`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	conn, err := second.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=50`); err != nil {
		t.Fatal(err)
	}
	other, err := conn.BeginTx(ctx, nil)
	if err == nil {
		other.Rollback()
		t.Fatal("second writer began while the first held its snapshot")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
		t.Fatalf("contending begin = %v, want SQLITE_BUSY (5)", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE counter SET value=value+1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	other, err = conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	if err := other.QueryRowContext(ctx, `SELECT value FROM counter`).Scan(&value); err != nil || value != 1 {
		t.Fatalf("next writer snapshot = %d, %v; want committed value 1", value, err)
	}
	if _, err := other.ExecContext(ctx, `UPDATE counter SET value=value+1`); err != nil {
		t.Fatal(err)
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyTransactionDoesNotReserveWriter(t *testing.T) {
	first, second := contentionDatabase(t)
	ctx := context.Background()
	tx, err := first.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var value int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM counter`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecContext(ctx, `UPDATE counter SET value=value+1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM counter`).Scan(&value); err != nil || value != 0 {
		t.Fatalf("reader snapshot = %d, %v; want original value 0", value, err)
	}
}
