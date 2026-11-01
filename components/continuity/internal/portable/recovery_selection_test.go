package portable

import (
	"database/sql"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"testing"
	"time"
)

func TestGuardedRecoveryComparisonsUseValidatedExactIdentities(t *testing.T) {
	original, _, recovery := recoveryInvalidationFixture(t)
	ctx, db, project := operationalProject(t, "guarded-recovery-selection")
	if _, err := (Service{DB: db}).Initialize(ctx, project, original.Envelope.LineageID); err != nil {
		t.Fatal(err)
	}
	spec, err := DecodeMemoryEnvelope(recovery.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	plan, receipt, err := ImportMemoryRecords(ctx, db, project, []EnvelopeSpec{spec}, time.Unix(1, 0))
	if err != nil || !plan.ApplyAllowed || receipt == nil {
		t.Fatalf("correction import: %+v %v", plan, err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if known, err := RecoveredEnvelopes(ctx, tx, project); err != nil || len(known) != 1 {
		t.Fatalf("baseline lookup validation: %v %v", known, err)
	}
	source, err := recoveryComparisonReader(tx, RecoveryComparisonGuarded)
	if err != nil {
		t.Fatal(err)
	}
	measured, metrics := syncwork.WithWorkMetrics(ctx)
	unrelated := Envelope{RecordType: "decision", RecordID: "new"}
	anotherRevision := original.Envelope
	anotherRevision.RecordRevision++
	if allowed, err := recoverySelectionAllowed(measured, source, project, []Envelope{unrelated, unrelated, anotherRevision}); err != nil || !allowed || metrics.Counts()["recovery_identity_probes"] != 2 {
		t.Fatalf("unrelated/revision selection: %t %v %v", allowed, err, metrics.Counts())
	}
	if allowed, err := recoverySelectionAllowed(ctx, source, project, []Envelope{original.Envelope}); err != nil || allowed {
		t.Fatalf("affected correction omitted: %t %v", allowed, err)
	}
	if allowed, err := recoverySelectionAllowed(ctx, tx, project, []Envelope{unrelated}); err != nil || allowed {
		t.Fatalf("unguarded caller omitted complete recovery graph: %t %v", allowed, err)
	}
	if _, err := recoveryComparisonReader(tx, RecoveryComparison(255)); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// A malformed lookup header must never establish the complete baseline
	// required to trust this index. Mutations are fixture-only corruption.
	if _, err := db.Exec(`DROP TRIGGER redaction_recoveries_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE redaction_recoveries SET record_id='wrong' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoveredEnvelopes(ctx, db, project); err == nil {
		t.Fatal("recovery lookup metadata was not checked against proof")
	}
}
