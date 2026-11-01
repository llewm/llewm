package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestCanonicalTrackingRejectsMissingModifiedAndUnknownGuards(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "canonical-tracking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(want bool) {
		t.Helper()
		known, err := HistoryCanonicalTracking(ctx, db)
		if err != nil || known != want {
			t.Fatalf("tracking=%t want=%t %v", known, want, err)
		}
	}
	check(true)
	if _, err := db.Exec(`CREATE TRIGGER history_sync_unknown AFTER INSERT ON projects BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := db.Exec(`DROP TRIGGER history_sync_unknown`); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := db.Exec(`DROP TRIGGER history_journal_insert`); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := db.Exec(`CREATE TRIGGER history_journal_insert AFTER INSERT ON history_sync_changes BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestCanonicalTrackingRestorationCannotReusePriorCursorOrProof(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "restored-tracking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','now')`); err != nil {
		t.Fatal(err)
	}
	before, _, _, err := ReadSyncChanges(ctx, db, "p", "fixture", "state", 10)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := RememberSyncCursor(ctx, db, before, "state"); err != nil || !ok {
		t.Fatalf("initial receipt: %t %v", ok, err)
	}
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='history_sync_decision_records_insert'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER history_sync_decision_records_insert`); err != nil {
		t.Fatal(err)
	}
	if _, _, reason, err := ReadSyncChanges(ctx, db, "p", "fixture", "state", 10); err != nil || reason != "unknown_tracking" {
		t.Fatalf("missing tracking: %q %v", reason, err)
	}
	if _, err := SyncGeneration(ctx, db, "p"); !errors.Is(err, ErrCanonicalTracking) {
		t.Fatalf("missing generation guards accepted: %v", err)
	}
	if ok, err := RememberSyncCursor(ctx, db, before, "state"); err != nil || ok {
		t.Fatalf("unknown tracking acknowledged: %t %v", ok, err)
	}
	if err := RememberSyncProof(ctx, db, "p", "route", before.Generation, "tree", "archive"); err != nil {
		t.Fatal(err)
	}
	var proofs int
	if err := db.QueryRow(`SELECT count(*) FROM history_sync_proofs`).Scan(&proofs); err != nil || proofs != 0 {
		t.Fatalf("unknown tracking saved proof: %d %v", proofs, err)
	}
	if _, err := db.Exec(definition); err != nil {
		t.Fatal(err)
	}
	// Definitions are valid again, but the missing interval could have hidden
	// canonical writes. The schema component invalidates the old receipt even
	// when its journal sequence and raw generation tokens did not change.
	after, _, reason, err := ReadSyncChanges(ctx, db, "p", "fixture", "state", 10)
	if err != nil || reason != "global_change" || before.Generation == after.Generation || before.Sequence != after.Sequence {
		t.Fatalf("restored receipt: before=%+v after=%+v %q %v", before, after, reason, err)
	}
	if ok, err := RememberSyncCursor(ctx, db, before, "state"); err != nil || ok {
		t.Fatalf("stale receipt survived restoration: %t %v", ok, err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := SyncSnapshot(ctx, tx, "p", "fixture"); err != nil {
		t.Fatal(err)
	}
}
