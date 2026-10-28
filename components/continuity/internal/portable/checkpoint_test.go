package portable

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

func TestCheckpointFamilyRoundTripsWithImmutableActionHistory(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "checkpoint-source")
	lineage := "lineage:cccccccccccccccccccccccccccccccc"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sourceDBPath(t, sourceDB))
	service := checkpoint.Service{DB: sourceDB}
	first, err := service.CreateWithActions(ctx, checkpoint.Checkpoint{
		ID: "checkpoint-first", ProjectID: sourceProject, SessionID: "session-checkpoint", TaskID: "portable-checkpoints",
		RestartResume: &checkpoint.RestartResume{InstanceID: identity.InstanceID, Client: "codex"},
		Task:          "Portable checkpoints", Status: "in_progress", Summary: "Root " + root + " project " + sourceProject + " token=checkpoint-secret",
		NextAction: "Recover the checkpoint graph", Completed: []string{"Mapped checkpoint fields"},
		Attempts: []checkpoint.Attempt{{Description: "Use mutable state", Outcome: "Rejected", Reason: "History must remain immutable"}},
		Changes:  []string{"components/continuity/internal/portable/checkpoint.go"}, Verified: []string{"Fixture created"},
		EvidenceRefs: []string{"evidence:portable-checkpoint"}, Metadata: map[string]any{"source_root": root, "local_project": sourceProject, "nested": []any{"secret=metadata-secret"}},
		CodeHeads: []checkpoint.CodeHead{{Commit: strings.Repeat("c", 40), Branch: "feature/portable"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateWithActions(ctx, checkpoint.Checkpoint{
		ID: "checkpoint-second", ProjectID: sourceProject, TaskID: "portable-checkpoints", PredecessorCheckpointID: "checkpoint-first", Task: "Portable checkpoints follow-up",
		Status: "paused", Summary: "Carry the earlier action explicitly", NextAction: "Resume after recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	var firstActionID, secondActionID string
	if err := sourceDB.QueryRowContext(ctx, `SELECT id FROM checkpoint_actions WHERE source_checkpoint_id=?`, first.Checkpoint.ID).Scan(&firstActionID); err != nil {
		t.Fatal(err)
	}
	if err := sourceDB.QueryRowContext(ctx, `SELECT id FROM checkpoint_actions WHERE source_checkpoint_id=?`, second.Checkpoint.ID).Scan(&secondActionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransitionAction(ctx, sourceProject, "", secondActionID, "superseded", "Replaced by reviewed work"); err != nil {
		t.Fatal(err)
	}
	terminal, err := service.CreateWithReconciliation(ctx, checkpoint.Checkpoint{
		ID: "checkpoint-terminal", ProjectID: sourceProject, TaskID: "portable-checkpoints", PredecessorCheckpointID: "checkpoint-second", Task: "Portable checkpoints complete",
		Status: "completed", Summary: "Recovered the checkpoint graph", NextAction: "Task complete; await the next objective.",
	}, checkpoint.ActionReconciliation{Updates: []checkpoint.ActionUpdate{{ID: firstActionID, Status: "completed", Reason: "token=transition-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.Reconciled) != 1 || terminal.Reconciled[0].ID != firstActionID {
		t.Fatalf("checkpoint-scoped terminal transition was not recorded: %#v", terminal)
	}

	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	counts := countSpecs(loaded.Records)
	if counts["checkpoint"] != 3 || counts["checkpoint_action"] != 2 || counts["checkpoint_action_transition"] != 5 || counts["checkpoint_context"] != 3 || counts["checkpoint_branches"] != 1 || len(loaded.Records) != 14 {
		t.Fatalf("unexpected checkpoint family export counts: %#v", counts)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:checkpoints", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{root, sourceProject, "checkpoint-secret", "metadata-secret", "transition-secret", "restart_resume"} {
		if bytes.Contains(bundle.RecordsBytes, []byte(leaked)) {
			t.Fatalf("checkpoint bundle leaked %q", leaked)
		}
	}
	if !bytes.Contains(bundle.RecordsBytes, []byte("[PROJECT_ROOT]")) || !bytes.Contains(bundle.RecordsBytes, []byte("[PROJECT_ID]")) || !bytes.Contains(bundle.RecordsBytes, []byte("[REDACTED]")) {
		t.Fatalf("checkpoint bundle omitted redaction markers: %s", bundle.RecordsBytes)
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "checkpoint-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != 14 || plan.WritesPerformed != 0 {
		t.Fatalf("checkpoint plan mismatch: %#v err=%v", plan, err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, time.Date(2026, 9, 19, 6, 1, 0, 0, time.UTC))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != 14 || applied.Plan.WritesPerformed != 15 {
		t.Fatalf("checkpoint apply mismatch: %#v err=%v", applied, err)
	}
	for table, want := range map[string]int{"checkpoints": 3, "checkpoint_actions": 2, "checkpoint_action_transitions": 5, "checkpoint_branches": 1} {
		if got := tableCount(t, targetDB, table); got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
	unresolved, err := (checkpoint.Service{DB: targetDB}).Unresolved(targetCtx, targetProject, "portable-checkpoints", "")
	if err != nil || len(unresolved) != 0 {
		t.Fatalf("terminal action history changed after import: %#v err=%v", unresolved, err)
	}
	latest, err := (checkpoint.Service{DB: targetDB}).Latest(targetCtx, targetProject)
	if err != nil || latest == nil || latest.ID != "checkpoint-terminal" || latest.ProjectID != targetProject || latest.Status != "completed" {
		t.Fatalf("recovered checkpoint mismatch: %#v err=%v", latest, err)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:checkpoint-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: time.Date(2026, 9, 19, 6, 2, 0, 0, time.UTC), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundle.RecordsBytes, reexport.RecordsBytes) || bundle.Manifest.RecordSetSHA256 != reexport.Manifest.RecordSetSHA256 {
		t.Fatal("checkpoint clone re-export changed semantic records")
	}
	replay, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, time.Date(2026, 9, 19, 6, 3, 0, 0, time.UTC))
	if err != nil || replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != 14 || replay.Plan.WritesPerformed != 1 {
		t.Fatalf("checkpoint replay mismatch: %#v err=%v", replay, err)
	}
}

func TestCheckpointFamilyCollisionAndMissingDependencyAreWriteFree(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "checkpoint-dependency-source")
	lineage := "lineage:dddddddddddddddddddddddddddddddd"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (checkpoint.Service{DB: sourceDB}).Create(ctx, checkpoint.Checkpoint{ID: "checkpoint-shared", ProjectID: sourceProject, TaskID: "shared", Task: "Source", Status: "in_progress", Summary: "Source", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:checkpoint-source", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Date(2026, 9, 19, 7, 0, 0, 0, time.UTC), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	isolationCtx, isolationDB, isolationProject := operationalProject(t, "checkpoint-isolated-lineage")
	if _, err := (Service{DB: isolationDB}).Initialize(isolationCtx, isolationProject, "lineage:ffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOperationalImport(isolationCtx, isolationDB, isolationProject, bundle.ManifestBytes, bundle.RecordsBytes); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("checkpoint bundle crossed an isolated lineage: %v", err)
	}
	if got := checkpointTableTotal(t, isolationDB); got != 0 {
		t.Fatalf("isolated-lineage rejection wrote %d rows", got)
	}

	collisionCtx, collisionDB, collisionProject := operationalProject(t, "checkpoint-collision")
	if _, err := (Service{DB: collisionDB}).Initialize(collisionCtx, collisionProject, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (checkpoint.Service{DB: collisionDB}).Create(collisionCtx, checkpoint.Checkpoint{ID: "checkpoint-shared", ProjectID: collisionProject, TaskID: "shared", Task: "Different", Status: "paused", Summary: "Different", NextAction: "Stop"}); err != nil {
		t.Fatal(err)
	}
	before := checkpointTableTotal(t, collisionDB)
	plan, err := PlanOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes)
	// The checkpoint and its context both collide.
	if err != nil || plan.ApplyAllowed || plan.Counts[PlanCollision] != 2 || plan.WritesPerformed != 0 {
		t.Fatalf("checkpoint collision plan mismatch: %#v err=%v", plan, err)
	}
	if _, err := ApplyOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes, time.Now()); err == nil {
		t.Fatal("checkpoint collision apply succeeded")
	}
	if after := checkpointTableTotal(t, collisionDB); after != before {
		t.Fatalf("checkpoint collision wrote rows: before=%d after=%d", before, after)
	}

	var actionOnly EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "checkpoint_action" {
			actionOnly = spec
			break
		}
	}
	if actionOnly.RecordID == "" {
		t.Fatal("fixture action not found")
	}
	missing, err := BuildBundle(BundleSpec{ExportID: "export:missing-checkpoint", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Date(2026, 9, 19, 7, 1, 0, 0, time.UTC), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: []Omission{}}, []EnvelopeSpec{actionOnly})
	if err != nil {
		t.Fatal(err)
	}
	emptyCtx, emptyDB, emptyProject := operationalProject(t, "checkpoint-empty")
	if _, err := (Service{DB: emptyDB}).Initialize(emptyCtx, emptyProject, lineage); err != nil {
		t.Fatal(err)
	}
	invalid, err := PlanOperationalImport(emptyCtx, emptyDB, emptyProject, missing.ManifestBytes, missing.RecordsBytes)
	if err != nil || invalid.ApplyAllowed || invalid.Counts[PlanInvalid] != 1 || invalid.WritesPerformed != 0 || !strings.Contains(invalid.Items[0].Reason, "missing source checkpoint") {
		t.Fatalf("missing checkpoint dependency was not classified invalid: %#v err=%v", invalid, err)
	}
	if got := checkpointTableTotal(t, emptyDB); got != 0 {
		t.Fatalf("invalid dependency wrote %d rows", got)
	}
}

func countSpecs(specs []EnvelopeSpec) map[string]int {
	counts := make(map[string]int)
	for _, spec := range specs {
		counts[spec.RecordType]++
	}
	return counts
}

func checkpointTableTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	return tableCount(t, db, "checkpoints") + tableCount(t, db, "checkpoint_actions") + tableCount(t, db, "checkpoint_action_transitions") + tableCount(t, db, "portable_import_receipts")
}

func TestCheckpointPortablePayloadRejectsUnredactedSecret(t *testing.T) {
	lineage := "lineage:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	payload, err := json.Marshal(checkpointPayload{Task: "Secret", Status: "paused", Summary: "token=not-redacted", NextAction: "Stop", Completed: []string{}, Attempts: []checkpoint.Attempt{}, Changes: []string{}, Verified: []string{}, EvidenceRefs: []string{}, OperationalSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:unredacted", LineageID: lineage, SourceInstanceID: "instance:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", CreatedAt: time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC), SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: []Omission{}}, []EnvelopeSpec{{LineageID: lineage, RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: "checkpoint-secret", RecordedAt: time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC), Authority: "caller_asserted", Index: json.RawMessage(`{"title":"Secret","status":"paused"}`), Payload: payload}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, db, projectID := operationalProject(t, "checkpoint-unredacted-target")
	if _, err := (Service{DB: db}).Initialize(ctx, projectID, lineage); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || plan.ApplyAllowed || plan.Counts[PlanInvalid] != 1 || !strings.Contains(plan.Items[0].Reason, "not redacted") {
		t.Fatalf("unredacted checkpoint was not rejected: %#v err=%v", plan, err)
	}
}
