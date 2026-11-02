package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestMembershipBatchingRequiresReviewedTriggerClosure(t *testing.T) {
	ctx := context.Background()
	db, err := Initialize(ctx, filepath.Join(t.TempDir(), "membership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	check := func(want bool) {
		t.Helper()
		got, err := HistoryIndexMembershipBatching(ctx, tx)
		if err != nil || got != want {
			t.Fatalf("batching=%t want%t: %v", got, want, err)
		}
	}
	check(true)
	for _, table := range []string{"history_index_block_members", "history_sync_changes", "history_sync_generation", "history_index_inventory_generation"} {
		for _, temporary := range []bool{false, true} {
			prefix := ""
			if temporary {
				prefix = "TEMP "
			}
			if _, err := tx.ExecContext(ctx, "CREATE "+prefix+"TRIGGER unknown_effect AFTER INSERT ON "+strings.ToUpper(table)+" BEGIN SELECT 1; END"); err != nil {
				t.Fatal(err)
			}
			check(false)
			if _, err := tx.ExecContext(ctx, "DROP TRIGGER unknown_effect"); err != nil {
				t.Fatal(err)
			}
			check(true)
		}
	}
	expected, err := membershipBatchDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range expected {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+name); err != nil {
			t.Fatal(err)
		}
		check(false)
		if _, err := tx.ExecContext(ctx, body); err != nil {
			t.Fatal(err)
		}
		check(true)
	}
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER history_journal_insert"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TRIGGER history_journal_insert AFTER INSERT ON history_sync_changes BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER history_journal_insert"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, expected["history_journal_insert"]); err != nil {
		t.Fatal(err)
	}
	// An unrelated trigger is outside the INSERT effect graph.
	if _, err := tx.ExecContext(ctx, "CREATE TRIGGER unrelated_effect AFTER INSERT ON decision_records BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	check(true)
}
