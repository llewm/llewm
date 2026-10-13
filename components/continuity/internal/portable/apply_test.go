package portable

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func operationalRecoveryBundle(t *testing.T) (Bundle, string, time.Time) {
	t.Helper()
	ctx, db, projectID := operationalProject(t, "recovery-source")
	lineage := "lineage:66666666666666666666666666666666"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 18, 18, 0, 0, 0, time.UTC)
	invocation, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{
		ID: "invocation-recovery", ProjectID: projectID, SessionID: "session-recovery",
		ActorType: "agent", Client: "codex", Provider: "openai", Model: "model-recovery",
		IdentitySource: "client_hook_claim", OccurredAt: recorded, ReceivedAt: recorded,
		EffortRequested: &attribution.Effort{Raw: "xhigh", Normalized: "xhigh", Source: "request_metadata", Authority: "client_claim"},
		EffortEffective: &attribution.Effort{Raw: "provider-high", Source: "provider_response", Authority: "provider_reported"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sourceDBPath(t, db))
	service := worknarrative.Service{DB: db, Now: func() time.Time { return recorded }}
	narrative := func(id, title, text, supersedes string) worknarrative.Record {
		return worknarrative.Record{ID: id, ProjectID: projectID, Title: title, Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: text, Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "task", Target: "recovery", Authority: "caller_asserted"}}, Creator: "codex", CreatorInvocationID: invocation.ID, SupersedesID: supersedes}
	}
	first, err := service.Append(ctx, narrative("narrative-original", "Original", "Original at "+root, ""))
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return recorded.Add(time.Minute) }
	if _, err := service.Append(ctx, narrative("narrative-correction", "Correction", "Corrected detail", first.ID)); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:recovery", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(2 * time.Minute), SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}, Omissions: []Omission{}}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Records) != 4 {
		t.Fatalf("expected invocation, two narratives, and supersession edge; got %d", len(bundle.Records))
	}
	return bundle, lineage, recorded
}

func TestApplyOperationalImportRecoversAtomicallyAndReexportsStableRecords(t *testing.T) {
	bundle, lineage, recorded := operationalRecoveryBundle(t)
	ctx, db, projectID := operationalProject(t, "recovery-target")
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil || result.Receipt.AppendedRecords != 4 || result.Plan.WritesPerformed != 5 || !result.Plan.ApplyAllowed {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if got := tableCount(t, db, "actor_invocations"); got != 1 {
		t.Fatalf("actor count=%d", got)
	}
	if got := tableCount(t, db, "work_narratives"); got != 2 {
		t.Fatalf("narrative count=%d", got)
	}
	if got := tableCount(t, db, "portable_import_receipts"); got != 1 {
		t.Fatalf("receipt count=%d", got)
	}
	var supersedes, creator string
	if err := db.QueryRow(`SELECT supersedes_id,creator_invocation_id FROM work_narratives WHERE id='narrative-correction'`).Scan(&supersedes, &creator); err != nil || supersedes != "narrative-original" || creator != "invocation-recovery" {
		t.Fatalf("recovered links mismatch: supersedes=%q creator=%q err=%v", supersedes, creator, err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:clone", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(4 * time.Minute), SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: bundle.Manifest.RedactionProfile, Omissions: []Omission{}}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundle.RecordsBytes, reexport.RecordsBytes) || bundle.Manifest.RecordSetSHA256 != reexport.Manifest.RecordSetSHA256 {
		t.Fatal("clone re-export changed portable semantic records")
	}
	replay, err := ApplyOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Receipt == nil || replay.Receipt.AppendedRecords != 0 || replay.Receipt.AlreadyPresentRecords != 4 || replay.Plan.WritesPerformed != 1 || tableCount(t, db, "portable_import_receipts") != 2 {
		t.Fatalf("replay was not idempotent: %#v", replay)
	}
}

func TestApplyOperationalImportRefusesCollisionWithoutReceipt(t *testing.T) {
	bundle, lineage, recorded := operationalRecoveryBundle(t)
	ctx, db, projectID := operationalProject(t, "collision-target")
	if _, err := (Service{DB: db}).Initialize(ctx, projectID, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: "invocation-recovery", ProjectID: projectID, ActorType: "agent", Model: "different", IdentitySource: "caller_asserted", OccurredAt: recorded, ReceivedAt: recorded}); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(time.Hour))
	if err == nil || result.Plan.Counts[PlanCollision] != 1 || result.Receipt != nil {
		t.Fatalf("collision was not refused: %#v err=%v", result, err)
	}
	if tableCount(t, db, "work_narratives") != 0 || tableCount(t, db, "portable_import_receipts") != 0 {
		t.Fatal("collision wrote narratives or receipt")
	}
}

func TestApplyOperationalImportRollsBackWhenReceiptFails(t *testing.T) {
	bundle, lineage, recorded := operationalRecoveryBundle(t)
	ctx, db, projectID := operationalProject(t, "rollback-target")
	if _, err := (Service{DB: db}).Initialize(ctx, projectID, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_import_receipt BEFORE INSERT ON portable_import_receipts BEGIN SELECT RAISE(ABORT, 'fixture receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(time.Hour))
	if err == nil || result.Receipt != nil {
		t.Fatalf("receipt failure was accepted: %#v err=%v", result, err)
	}
	for _, table := range []string{"actor_invocations", "work_narratives", "work_narrative_anchors", "portable_import_receipts"} {
		if got := tableCount(t, db, table); got != 0 {
			t.Fatalf("%s retained %d rows after rollback", table, got)
		}
	}
}

func tableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
