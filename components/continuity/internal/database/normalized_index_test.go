package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizedIndexMigrationPreservesIntentAndUncoveredData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	db := createDatabaseThrough(t, path, "088_history_dependency_index.sql")
	for _, q := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/fixture','fixture','2026-10-31T00:00:00Z')`,
		`INSERT INTO history_index_blocks VALUES('p','first',5),('p','second',5),('p','obsolete',4),('p','uncovered',4)`,
		`INSERT INTO history_index_publications VALUES('p','endpoint','first'),('p','staged-intent','second')`,
		`INSERT INTO history_index_records VALUES('p','first','a','decision','a',1,'body-a'),('p','second','a','decision','a',1,'body-a'),('p','second','b','decision','b',1,'body-b'),('p','obsolete','a','decision','a',1,'body-a'),('p','uncovered','c','decision','c',1,'body-c')`,
		`INSERT INTO history_index_refs VALUES('p','first','a','decision','target'),('p','second','a','decision','target'),('p','obsolete','a','decision','target')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	receipt, err := Migrate(ctx, path, MigrateOptions{From: "088_history_dependency_index.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before.db")})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BackupIntegrity != "ok" || receipt.DatabaseIntegrity != "ok" || receipt.ForeignKeyViolations != 0 {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		`SELECT count(*) FROM history_index_envelopes`:                                               3,
		`SELECT count(*) FROM history_index_block_members`:                                           4,
		`SELECT count(*) FROM history_index_refs`:                                                    1,
		`SELECT count(*) FROM history_index_publications`:                                            2,
		`SELECT count(*) FROM history_index_blocks WHERE block_hash='obsolete'`:                      0,
		`SELECT count(*) FROM history_index_blocks WHERE block_hash='uncovered' AND index_version=4`: 1,
		`SELECT count(*) FROM history_index_blocks WHERE index_version=6`:                            2,
		`SELECT mcp_api FROM writer_floor WHERE singleton=1`:                                         136,
	} {
		var got int
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got%d want%d err%v", q, got, want, err)
		}
	}
	// A shared body survives removal of one block, then is collected with its refs
	// only after the final membership disappears.
	if _, err := db.Exec(`DELETE FROM history_index_blocks WHERE block_hash='first'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM history_index_envelopes WHERE envelope_hash='a'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared body lost: %d %v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM history_index_blocks WHERE block_hash='second'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM history_index_refs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refs not collected: %d %v", count, err)
	}
}

func TestNormalizedIndexMigrationRejectsConflictingSharedBodies(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conflict.db")
	db := createDatabaseThrough(t, path, "088_history_dependency_index.sql")
	for _, q := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/fixture','fixture','2026-10-31T00:00:00Z')`,
		`INSERT INTO history_index_blocks VALUES('p','a',5),('p','b',5)`,
		`INSERT INTO history_index_records VALUES('p','a','same','decision','a',1,'body-a'),('p','b','same','decision','a',1,'body-b')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	_, err := Migrate(ctx, path, MigrateOptions{From: "088_history_dependency_index.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before.db")})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("conflicting cache accepted: %v", err)
	}
	status, err := Inspect(ctx, path)
	if err != nil || status.AppliedMigration != "088_history_dependency_index.sql" {
		t.Fatalf("failed migration changed schema: %+v %v", status, err)
	}
}
