package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDurableArchiveMigrationPreservesLegacyAndRequiresActivation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	db := createDatabaseThrough(t, path, "090_project_action_changes.sql")
	for _, query := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/fixture','fixture','now')`,
		`INSERT INTO project_memory_identities VALUES('p','lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','instance:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','generated','caller_asserted','now',1)`,
		`INSERT INTO history_index_blocks VALUES('p','staged',6)`,
		`INSERT INTO history_index_envelopes VALUES('p','old-observation','decision','id',1,'old exact bytes')`,
		`INSERT INTO history_index_block_members VALUES('p','staged','old-observation')`,
		`INSERT INTO history_index_publications VALUES('p','endpoint','staged')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	receipt, err := Migrate(ctx, path, MigrateOptions{From: "090_project_action_changes.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before.db")})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.BackupIntegrity != "ok" || receipt.DatabaseIntegrity != "ok" || receipt.ForeignKeyViolations != 0 {
		t.Fatalf("migration receipt: %+v", receipt)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for query, want := range map[string]int{
		`SELECT count(*) FROM history_archive_state`:         0,
		`SELECT count(*) FROM history_archive_envelopes`:     0,
		`SELECT count(*) FROM history_archive_origins`:       0,
		`SELECT count(*) FROM history_archive_changes`:       0,
		`SELECT count(*) FROM history_index_envelopes`:       1,
		`SELECT count(*) FROM history_index_publications`:    1,
		`SELECT count(*) FROM history_index_block_members`:   1,
		`SELECT mcp_api FROM writer_floor WHERE singleton=1`: 136,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got%d want%d %v", query, got, want, err)
		}
	}
	var body string
	if err := db.QueryRow(`SELECT envelope_json FROM history_index_envelopes`).Scan(&body); err != nil || body != "old exact bytes" {
		t.Fatalf("legacy bytes changed or promoted: %q %v", body, err)
	}
}
