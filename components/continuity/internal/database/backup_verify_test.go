package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPreparedBackupDigestRejectsValidDataChangesBeforeMigration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "source.db")
	from := "026_procedure_monitoring.sql"
	db := createDatabaseThrough(t, path, from)
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/fixture','original','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	backup := filepath.Join(root, "before.db")
	receipt, err := Backup(ctx, path, backup)
	if err != nil || len(receipt.BackupSHA256) != 64 {
		t.Fatalf("backup: %+v %v", receipt, err)
	}
	changed, err := openReadWrite(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changed.Exec(`UPDATE projects SET name='changed' WHERE id='p'`); err != nil {
		changed.Close()
		t.Fatal(err)
	}
	if integrity, err := integrityCheck(ctx, changed); err != nil || integrity != "ok" {
		changed.Close()
		t.Fatalf("fixture is not structurally valid: %s %v", integrity, err)
	}
	changed.Close()
	if _, err := Migrate(ctx, path, MigrateOptions{From: from, To: latestMigration, BackupPath: backup, VerifiedBackupSHA256: receipt.BackupSHA256}); err == nil {
		t.Fatal("schema changed despite a modified prepared backup")
	}
	status, err := Inspect(ctx, path)
	if err != nil || status.AppliedMigration != from {
		t.Fatalf("rejection changed source: %+v %v", status, err)
	}
	fresh := filepath.Join(root, "verified.db")
	verified, err := Backup(ctx, path, fresh)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := Migrate(ctx, path, MigrateOptions{From: from, To: latestMigration, BackupPath: fresh, VerifiedBackupSHA256: verified.BackupSHA256})
	if err != nil || migrated.BackupSHA256 != verified.BackupSHA256 || migrated.DatabaseIntegrity != "ok" {
		t.Fatalf("verified reuse: %+v %v", migrated, err)
	}
	if _, err := VerifyBackup(ctx, fresh, from, verified.BackupSHA256); err != nil {
		t.Fatalf("migration modified its prepared backup: %v", err)
	}
}
