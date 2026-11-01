package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestRadixMigrationFencesHeldOldWriterAndRetainsBackupFloor(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path, backup := filepath.Join(root, "before.db"), filepath.Join(root, "backup.db")
	old := createDatabaseThrough(t, path, "094_history_recovery_selection.sql")
	defer old.Close()
	if err := CheckWriter(ctx, old, 137); err != nil {
		t.Fatal(err)
	}
	receipt, err := Migrate(ctx, path, MigrateOptions{From: "094_history_recovery_selection.sql", To: "095_history_radix_writer.sql", BackupPath: backup})
	if err != nil || receipt.BackupIntegrity != "ok" || receipt.DatabaseIntegrity != "ok" || receipt.ForeignKeyViolations != 0 {
		t.Fatalf("migration: %+v %v", receipt, err)
	}
	var stale *StaleWriterError
	if err := CheckWriter(ctx, old, 137); !errors.As(err, &stale) || stale.Floor != 138 {
		t.Fatalf("held old writer not fenced: %v", err)
	}
	if err := CheckWriter(ctx, old, 138); err != nil {
		t.Fatal(err)
	}
	retained, err := sql.Open("sqlite", "file:"+filepath.ToSlash(backup)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	if floor, err := WriterFloor(ctx, retained); err != nil || floor != 137 {
		t.Fatalf("backup floor changed: %d %v", floor, err)
	}
}
