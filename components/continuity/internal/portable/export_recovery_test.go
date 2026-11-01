package portable

import (
	"bytes"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"testing"
	"time"
)

func TestMemoryDeltaScopesExistingRecoveryToSelectedIdentities(t *testing.T) {
	original, _, recovery := recoveryInvalidationFixture(t)
	ctx, db, project := operationalProject(t, "export-recovery-selection")
	if _, err := (Service{DB: db}).Initialize(ctx, project, original.Envelope.LineageID); err != nil {
		t.Fatal(err)
	}
	spec, err := DecodeMemoryEnvelope(recovery.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := ImportMemoryRecords(ctx, db, project, []EnvelopeSpec{spec}, time.Unix(1, 0))
	if err != nil || !plan.ApplyAllowed {
		t.Fatalf("proof import: %+v %v", plan, err)
	}
	baseline, err := EncodeMemoryExport(ctx, db, project)
	if err != nil || len(baseline.Envelopes) != 1 {
		t.Fatalf("complete baseline: %d %v", len(baseline.Envelopes), err)
	}
	cursor, _, _, err := database.ReadSyncChanges(ctx, db, project, "export-recovery", "archive", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := database.RememberSyncCursor(ctx, db, cursor, "archive"); err != nil || !ok {
		t.Fatalf("baseline receipt: %t %v", ok, err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "independent", Title: "Independent", Rationale: "Fixture", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	before, changes, reason, err := database.ReadSyncChanges(ctx, db, project, "export-recovery", "archive", 4096)
	if err != nil || reason != "" {
		t.Fatalf("input: %s %v", reason, err)
	}
	measured, metrics := syncwork.WithWorkMetrics(ctx)
	delta, receipt, used, err := EncodeMemoryDelta(measured, db, before, changes)
	counts := metrics.Counts()
	if err != nil || !used || len(delta.Envelopes) != 1 || counts["recovery_records_checked"] != 0 || counts["recovery_identity_probes"] != 1 || counts["export_retention_journal_rows_read"] != 1 || counts["export_retention_rows_checked"] != 1 {
		t.Fatalf("independent export scanned recovery graph: %t %v %v", used, err, counts)
	}
	if ok, err := database.RememberSyncCursor(ctx, db, receipt, "archive"); err != nil || !ok {
		t.Fatalf("delta receipt: %t %v", ok, err)
	}
	complete, err := EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, object := range complete.Envelopes {
		if object.Envelope.RecordID == "independent" {
			matched = bytes.Equal(object.Bytes, delta.Envelopes[0].Bytes)
		}
	}
	if !matched {
		t.Fatal("selected export differs from complete bytes")
	}
	// A later operational record at a corrected identity must take the complete
	// reader, even though the old proof does not grant or create that record.
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ProjectID: project, ID: original.Envelope.RecordID, Title: "New content at corrected identity", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "New content", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	before, changes, reason, err = database.ReadSyncChanges(ctx, db, project, "export-recovery", "archive", 4096)
	if err != nil || reason != "" {
		t.Fatalf("affected input: %s %v", reason, err)
	}
	measured, metrics = syncwork.WithWorkMetrics(ctx)
	if _, _, used, err := EncodeMemoryDelta(measured, db, before, changes); err != nil || used || metrics.Counts()["fallback_delta_recovery"] != 1 || metrics.Counts()["recovery_records_checked"] != 0 {
		t.Fatalf("affected correction partially exported: %t %v %v", used, err, metrics.Counts())
	}
	var retained int
	if err := db.QueryRow(`SELECT count(*) FROM portable_envelopes WHERE project_id=? AND record_type=? AND record_id=?`, project, original.Envelope.RecordType, original.Envelope.RecordID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("declined selection wrote retention: %d %v", retained, err)
	}
	if _, err := EncodeMemoryExport(ctx, db, project); err != nil {
		t.Fatalf("complete fallback failed: %v", err)
	}
}
