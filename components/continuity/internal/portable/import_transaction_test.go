package portable

import (
	"bytes"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestImportMemoryRecordsTxSharesCommitAndRollbackWithConsumerCursor(t *testing.T) {
	bundle, lineage, at := operationalRecoveryBundle(t)
	var specs []EnvelopeSpec
	expected := map[string][]byte{}
	for _, object := range bundle.Records {
		spec, err := DecodeMemoryEnvelope(object.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
		expected[recordKey(object.Envelope)] = object.Bytes
	}
	for _, mode := range []string{"commit", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, project := operationalProject(t, "transaction-import")
			if _, err := (Service{DB: db}).Initialize(ctx, project, lineage); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			plan, receipt, err := ImportMemoryRecordsTx(ctx, tx, project, specs, at)
			if err != nil || !plan.ApplyAllowed || receipt == nil || receipt.AppendedRecords != 4 || plan.WritesPerformed != 5 {
				t.Fatalf("staged import: %+v %+v %v", plan, receipt, err)
			}
			cursor, err := database.SyncSnapshot(ctx, tx, project, "transaction-fixture")
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := database.RememberSyncCursorWithin(ctx, tx, cursor, "materialized"); err != nil || !ok {
				t.Fatalf("staged cursor: %t %v", ok, err)
			}
			var staged int
			if err := tx.QueryRow(`SELECT count(*) FROM portable_import_receipts WHERE project_id=?`, project).Scan(&staged); err != nil || staged != 1 {
				t.Fatalf("staged receipt: %d %v", staged, err)
			}
			if mode == "rollback" {
				err = tx.Rollback()
			} else {
				err = tx.Commit()
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				for _, table := range []string{"work_narratives", "actor_invocations", "portable_envelopes", "portable_import_receipts", "history_sync_cursors"} {
					if count := tableCount(t, db, table); count != 0 {
						t.Fatalf("rollback leaked %s: %d", table, count)
					}
				}
				return
			}
			export, err := EncodeMemoryExport(ctx, db, project)
			if err != nil || len(export.Envelopes) != len(expected) {
				t.Fatalf("committed export: %d %v", len(export.Envelopes), err)
			}
			for _, object := range export.Envelopes {
				if !bytes.Equal(object.Bytes, expected[recordKey(object.Envelope)]) {
					t.Fatalf("transaction import changed wire bytes: %s", recordKey(object.Envelope))
				}
			}
			if _, pending, reason, err := database.ReadSyncChanges(ctx, db, project, "transaction-fixture", "materialized", 100); err != nil || reason != "" || len(pending) != 0 {
				t.Fatalf("consumer did not share import commit: %d %q %v", len(pending), reason, err)
			}
			repeat, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer repeat.Rollback()
			plan, receipt, err = ImportMemoryRecordsTx(ctx, repeat, project, specs, at)
			if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAlreadyPresent] != 4 || plan.WritesPerformed != 0 || receipt != nil {
				t.Fatalf("repeat staged writes: %+v %+v %v", plan, receipt, err)
			}
			if err := repeat.Commit(); err != nil {
				t.Fatal(err)
			}
			if count := tableCount(t, db, "portable_import_receipts"); count != 1 {
				t.Fatalf("repeat appended receipt: %d", count)
			}
		})
	}
}
