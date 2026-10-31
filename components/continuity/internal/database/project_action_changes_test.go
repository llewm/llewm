package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProjectActionChangesMigrationAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "actions.db")
	db := createDatabaseThrough(t, path, "089_normalized_history_index.sql")
	for _, query := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now'),('q','/q','q','now')`,
		`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES('cp','p','Task','in_progress','Summary','Next','{}','now'),('cq','q','Task','in_progress','Summary','Next','{}','now')`,
		`INSERT INTO checkpoint_actions VALUES('ap','p','cp','Next','now'),('aq','q','cq','Next','now')`,
		`INSERT INTO checkpoint_action_transitions VALUES('before','ap','cp','open',NULL,'now')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	receipt, err := Migrate(ctx, path, MigrateOptions{From: "089_normalized_history_index.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before.db")})
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
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	read := func(project string) (SyncCursor, []SyncChange) {
		t.Helper()
		cursor, changes, reason, err := ReadSyncChanges(ctx, db, project, "test", "archive", 100)
		if err != nil || reason != "" {
			t.Fatalf("%s changes: %s %v", project, reason, err)
		}
		return cursor, changes
	}
	for _, project := range []string{"p", "q"} {
		cursor, _, reason, err := ReadSyncChanges(ctx, db, project, "test", "archive", 100)
		if err != nil || reason != "missing_cursor" {
			t.Fatalf("initial cursor: %s %v", reason, err)
		}
		if ok, err := RememberSyncCursor(ctx, db, cursor, "archive"); err != nil || !ok {
			t.Fatalf("save cursor: %t %v", ok, err)
		}
	}
	// Ownership follows the action, just like the export query. A transition
	// without a carrying checkpoint is still attributable to the action project.
	exec(`INSERT INTO checkpoint_action_transitions VALUES('arrival','ap',NULL,'open',NULL,'now')`)
	p, changes := read("p")
	q, other := read("q")
	if len(changes) != 1 || changes[0].Table != "checkpoint_action_transitions" || changes[0].Key != `["arrival"]` || changes[0].Operation != "insert" || len(other) != 0 {
		t.Fatalf("transition leaked ownership: %v %v", changes, other)
	}
	for _, cursor := range []SyncCursor{p, q} {
		if ok, err := RememberSyncCursor(ctx, db, cursor, "archive"); err != nil || !ok {
			t.Fatalf("save cursor: %t %v", ok, err)
		}
	}
	exec(`UPDATE checkpoint_action_transitions SET reason=reason WHERE id='arrival'`)
	if next, changes := read("p"); next != p || len(changes) != 0 {
		t.Fatal("no-op invalidated capture")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE checkpoint_action_transitions SET status='completed' WHERE id='arrival'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if next, changes := read("p"); next != p || len(changes) != 0 {
		t.Fatal("rollback leaked journal writes")
	}
	// Reassociation must invalidate both the former and the new owner.
	exec(`UPDATE checkpoint_action_transitions SET action_id='aq',status='completed' WHERE id='arrival'`)
	_, changes = read("p")
	_, other = read("q")
	if len(changes) != 1 || changes[0].Operation != "update_old" || len(other) != 1 || other[0].Operation != "update_new" {
		t.Fatalf("reassociation: %v %v", changes, other)
	}
	exec(`DELETE FROM checkpoint_action_transitions WHERE id='arrival'`)
	_, changes = read("p")
	_, other = read("q")
	if len(changes) != 1 || len(other) != 2 || other[1].Operation != "delete" {
		t.Fatalf("deletion: %v %v", changes, other)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM checkpoint_action_transitions WHERE id='before'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration lost prior transition: %d %v", count, err)
	}
}

func TestProjectActionChangesMissingParentInvalidatesGlobally(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Model damaged/imported legacy ownership with FK enforcement disabled on
	// this one connection. Missing ownership must never become an empty delta.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	global := func() string {
		t.Helper()
		var token string
		if err := conn.QueryRowContext(ctx, `SELECT token FROM history_sync_generation WHERE project_id=''`).Scan(&token); err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, query := range []string{
		`INSERT INTO checkpoint_action_transitions VALUES('orphan','missing',NULL,'open',NULL,'now')`,
		`UPDATE checkpoint_action_transitions SET status='completed' WHERE id='orphan'`,
		`DELETE FROM checkpoint_action_transitions WHERE id='orphan'`,
	} {
		before := global()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
		if global() == before {
			t.Fatalf("unknown ownership did not invalidate: %s", query)
		}
	}
}
