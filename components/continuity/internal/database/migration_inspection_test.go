package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrationInspectionRequiresExactSchemaAndCannotWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pending.db")
	source := "026_procedure_monitoring.sql"
	db := createDatabaseThrough(t, path, source)
	db.Close()
	for _, expected := range []string{"", "025_atomic_completion_terminal.sql", latestMigration} {
		if opened, err := OpenForMigrationInspection(ctx, path, expected); err == nil {
			opened.Close()
			t.Fatalf("inspection accepted mismatched schema %q", expected)
		}
	}
	view, err := OpenForMigrationInspection(ctx, path, source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	if actual, err := LatestMigration(ctx, view); err != nil || actual != source {
		t.Fatalf("inspection migrated source: %q %v", actual, err)
	}
	if _, err := view.ExecContext(ctx, `CREATE TABLE should_not_exist(value TEXT)`); err == nil {
		t.Fatal("migration inspection permitted writes")
	}
	if opened, err := OpenReadOnly(ctx, path); err == nil {
		opened.Close()
		t.Fatal("maintenance route weakened normal current-schema gate")
	}
}
