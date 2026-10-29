package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSyncJournalIsolationRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	exec := func(statement string) {
		t.Helper()
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	generation := func(id string) string {
		t.Helper()
		g, err := SyncGeneration(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	count := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM history_sync_changes`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now'),('q','/q','q','now')`)
	p, q, n := generation("p"), generation("q"), count()
	exec(`UPDATE projects SET name=name WHERE id='p'`)
	if count() != n || generation("p") != p {
		t.Fatal("no-op generated work")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE projects SET name='rolled back' WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count() != n || generation("p") != p {
		t.Fatal("rollback leaked tracking")
	}
	exec(`UPDATE projects SET name='changed' WHERE id='q'`)
	if generation("p") != p || generation("q") == q {
		t.Fatal("project isolation failed")
	}
	if err := RememberSyncProof(ctx, db, "p", "route", p, "tree", "archive"); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE projects SET name='changed' WHERE id='p'`)
	if err := RememberSyncProof(ctx, db, "p", "stale", p, "tree", "archive"); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_sync_proofs WHERE route='stale'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("stale proof saved: %d %v", stale, err)
	}
	current, entries := generation("p"), count()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if generation("p") != current || count() != entries {
		t.Fatal("restart lost committed tracking")
	}
	// Tracking corruption forces all project proofs to miss.
	q = generation("q")
	exec(`DELETE FROM history_sync_changes WHERE sequence=(SELECT MIN(sequence) FROM history_sync_changes)`)
	if generation("p") == current || generation("q") == q {
		t.Fatal("journal damage did not invalidate globally")
	}
	exec(`DELETE FROM history_sync_generation WHERE project_id='p'`)
	if _, err := SyncGeneration(ctx, db, "p"); err == nil {
		t.Fatal("missing generation accepted")
	}
}

func TestSyncJournalTracksBothSidesOfIdentityChanges(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now'),('q','/q','q','now')`,
		`INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES('p','old',1)`,
		`UPDATE history_index_blocks SET project_id='q',block_hash='new' WHERE project_id='p'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(`SELECT project_id,row_key,operation FROM history_sync_changes WHERE table_name='history_index_blocks' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := [][3]string{{"p", `["p","old"]`, "insert"}, {"p", `["p","old"]`, "update_old"}, {"q", `["q","new"]`, "update_new"}}
	i := 0
	for rows.Next() {
		var got [3]string
		if err := rows.Scan(&got[0], &got[1], &got[2]); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) || got != want[i] {
			t.Fatalf("entry%d: %v", i, got)
		}
		i++
	}
	if err := rows.Err(); err != nil || i != len(want) {
		t.Fatalf("entries=%d err=%v", i, err)
	}
}

func TestSyncJournalMigrationPreservesRecordsAndDropsOldProofs(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(ctx, db, migrationsBefore("086_history_change_journal.sql")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','preserve','now')`,
		`INSERT INTO history_sync_proofs(project_id,route,generation,tree,archive) VALUES('p','r','old','t','a')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	var name string
	var count int
	if err := db.QueryRow(`SELECT name FROM projects WHERE id='p'`).Scan(&name); err != nil || name != "preserve" {
		t.Fatalf("record lost: %q %v", name, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_sync_proofs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old proofs retained: %d %v", count, err)
	}
	if _, err := SyncGeneration(ctx, db, "p"); err != nil {
		t.Fatal(err)
	}
}
