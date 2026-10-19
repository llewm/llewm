package portable

import (
	"bytes"
	"database/sql"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/friction"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func TestReviewFamiliesRoundTripWithoutPortableFrictionCandidates(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "review-source")
	lineage := "lineage:78787878787878787878787878787878"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Dir(sourceDBPath(t, sourceDB))
	recorded := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	items := workitem.Service{DB: sourceDB, Now: func() time.Time { return recorded }}
	integrated, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "portable-review", ProjectID: sourceProject, Kind: "idea", Title: "Review at " + sourceRoot, Body: "Preserve " + sourceProject + " token=idea-secret", TaskID: "portable-memory", CheckpointID: "checkpoint-review", Creator: "codex", Model: "model-a", Tags: []string{"portable", "review"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Dispose(ctx, workitem.Disposition{ProjectID: sourceProject, ItemID: integrated.ID, Status: "accepted", Reason: "Plan token=disposition-secret", Creator: "human-reviewer", Model: "model-a", TargetRefs: []string{"roadmap:continuity-portable-memory"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Dispose(ctx, workitem.Disposition{ProjectID: sourceProject, ItemID: integrated.ID, Status: "integrated", Reason: "Applied token=disposition-secret", Creator: "human-reviewer", Model: "model-a", TargetRefs: []string{"commit:" + operationalCommit}}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "pending-review", ProjectID: sourceProject, Kind: "idea", Title: "Pending review", Body: "Keep this bounded", Creator: "human", Tags: []string{}}}); err != nil {
		t.Fatal(err)
	}

	store := activity.Store{DB: sourceDB}
	base := recorded.Add(-time.Hour)
	for i, client := range []string{"codex", "claude-code"} {
		session := fmt.Sprintf("review-session-%d", i)
		at := base.Add(time.Duration(i) * time.Minute)
		appendPortableReviewTool(t, store, sourceProject, fmt.Sprintf("review-failure-%d", i), session, client, "go test ./...", "failure", "cache permission denied token=event-secret", at)
		appendPortableReviewTool(t, store, sourceProject, fmt.Sprintf("review-recovery-%d", i), session, client, "GOCACHE=/tmp/cache go test ./...", "", "", at.Add(time.Second))
	}
	frictionService := friction.Service{DB: sourceDB, Now: func() time.Time { return recorded.Add(time.Minute) }}
	report, err := frictionService.Report(ctx, sourceProject, friction.Options{MinCount: 2, MaxBytes: 2048})
	if err != nil || len(report.Candidates) != 1 {
		t.Fatalf("friction fixture mismatch: %#v err=%v", report, err)
	}
	candidateID := report.Candidates[0].ID
	items.Now = func() time.Time { return recorded.Add(2 * time.Minute) }
	bug, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "cache-bug", ProjectID: sourceProject, Kind: "bug", Title: "Cache permissions", Body: "Confirm permissions", Observed: "permission denied", Severity: "low", Creator: "human-reviewer"}, From: "friction:" + candidateID})
	if err != nil {
		t.Fatal(err)
	}
	research, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "cache-question", ProjectID: sourceProject, Kind: "research", Title: "Why does the cache fail?", Body: "Find the cause", Creator: "human-reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: sourceDB}).Append(ctx, worknarrative.Record{ID: "cache-cause", ProjectID: sourceProject, Title: "Findings", Creator: "test", Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Permissions", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Dispose(ctx, workitem.Disposition{ProjectID: sourceProject, ItemID: research.ID, Status: "answered", Reason: "Permissions", TargetRefs: []string{"narrative:cache-cause"}, Creator: "human-reviewer"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	counts := countSpecs(loaded.Records)
	for recordType, want := range map[string]int{"work_item": 4, "work_item_disposition": 3, "work_item_link": 1} {
		if counts[recordType] != want {
			t.Fatalf("%s count=%d want=%d; all=%#v", recordType, counts[recordType], want, counts)
		}
	}
	for _, omission := range CuratedV1Omissions() {
		if omission.RecordFamily == "work_items" {
			t.Fatalf("implemented review family remains omitted: %#v", omission)
		}
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:review-families", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(3 * time.Minute), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{sourceRoot, sourceProject, "idea-secret", "disposition-secret", "event-secret", "review-failure-0", "GOCACHE=/tmp/cache"} {
		if bytes.Contains(bundle.RecordsBytes, []byte(leaked)) {
			t.Fatalf("review bundle leaked or exported derived source %q", leaked)
		}
	}
	if !bytes.Contains(bundle.RecordsBytes, []byte("[PROJECT_ROOT]")) || !bytes.Contains(bundle.RecordsBytes, []byte("[PROJECT_ID]")) || !bytes.Contains(bundle.RecordsBytes, []byte("[REDACTED]")) {
		t.Fatalf("review bundle omitted redaction markers: %s", bundle.RecordsBytes)
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "review-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != len(bundle.Records) || plan.WritesPerformed != 0 {
		t.Fatalf("review plan mismatch: %#v err=%v", plan, err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(4*time.Minute))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("review apply mismatch: %#v err=%v", applied, err)
	}
	for table, want := range map[string]int{"work_items": 4, "work_item_tags": 2, "work_item_dispositions": 3, "work_item_links": 1, "events": 0} {
		if got := tableCount(t, targetDB, table); got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
	shown, err := (workitem.Service{DB: targetDB}).Show(targetCtx, targetProject, integrated.ID, 4096)
	if err != nil || shown == nil || shown.Metadata.Status != "integrated" || !strings.Contains(shown.Markdown, "Plan token=[REDACTED]") || !strings.Contains(shown.Markdown, "Applied token=[REDACTED]") || !strings.Contains(shown.Markdown, "roadmap:continuity-portable-memory") || !strings.Contains(shown.Markdown, "commit:"+operationalCommit) {
		t.Fatalf("recovered idea mismatch: %#v err=%v", shown, err)
	}
	recoveredBug, err := (workitem.Service{DB: targetDB}).Get(targetCtx, targetProject, bug.ID)
	if err != nil || recoveredBug == nil || recoveredBug.Severity != "low" || len(recoveredBug.Links) != 1 || recoveredBug.Links[0].TargetRef != "friction:"+candidateID {
		t.Fatalf("recovered friction-tracking bug mismatch: %#v err=%v", recoveredBug, err)
	}
	if answered, err := (workitem.Service{DB: targetDB}).Get(targetCtx, targetProject, research.ID); err != nil || answered == nil || answered.Kind != "research" || answered.Status != "answered" {
		t.Fatalf("recovered research item mismatch: %#v err=%v", answered, err)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:review-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(5 * time.Minute), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundle.RecordsBytes, reexport.RecordsBytes) || bundle.Manifest.RecordSetSHA256 != reexport.Manifest.RecordSetSHA256 {
		t.Fatal("review clone re-export changed semantic records")
	}
	replay, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(6*time.Minute))
	if err != nil || replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != len(bundle.Records) || replay.Plan.WritesPerformed != 1 {
		t.Fatalf("review replay mismatch: %#v err=%v", replay, err)
	}
}

func TestReviewFamiliesRejectIsolationCollisionAndMissingItem(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "review-boundaries-source")
	lineage := "lineage:90909090909090909090909090909090"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	service := workitem.Service{DB: sourceDB, Now: func() time.Time { return recorded }}
	created, err := service.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "shared-idea", ProjectID: sourceProject, Kind: "idea", Title: "Source", Body: "Source", Creator: "human"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dispose(ctx, workitem.Disposition{ProjectID: sourceProject, ItemID: created.ID, Status: "discarded", Reason: "Reviewed", Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:review-boundaries", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}

	isolationCtx, isolationDB, isolationProject := operationalProject(t, "review-isolated")
	if _, err := (Service{DB: isolationDB}).Initialize(isolationCtx, isolationProject, "lineage:91919191919191919191919191919191"); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOperationalImport(isolationCtx, isolationDB, isolationProject, bundle.ManifestBytes, bundle.RecordsBytes); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("review bundle crossed isolated lineage: %v", err)
	}

	collisionCtx, collisionDB, collisionProject := operationalProject(t, "review-collision")
	if _, err := (Service{DB: collisionDB}).Initialize(collisionCtx, collisionProject, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (workitem.Service{DB: collisionDB, Now: func() time.Time { return recorded }}).Capture(collisionCtx, workitem.Capture{Item: workitem.Item{ID: created.ID, ProjectID: collisionProject, Kind: "idea", Title: "Different", Body: "Different", Creator: "human"}}); err != nil {
		t.Fatal(err)
	}
	before := reviewTableTotal(t, collisionDB)
	plan, err := PlanOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || plan.ApplyAllowed || plan.Counts[PlanCollision] != 1 {
		t.Fatalf("review collision mismatch: %#v err=%v", plan, err)
	}
	if _, err := ApplyOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded); err == nil {
		t.Fatal("review collision apply succeeded")
	}
	if after := reviewTableTotal(t, collisionDB); after != before {
		t.Fatalf("review collision wrote rows: before=%d after=%d", before, after)
	}

	var disposition EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "work_item_disposition" {
			disposition = spec
			break
		}
	}
	missing, err := BuildBundle(BundleSpec{ExportID: "export:review-missing", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: []Omission{}}, []EnvelopeSpec{disposition})
	if err != nil {
		t.Fatal(err)
	}
	emptyCtx, emptyDB, emptyProject := operationalProject(t, "review-empty")
	if _, err := (Service{DB: emptyDB}).Initialize(emptyCtx, emptyProject, lineage); err != nil {
		t.Fatal(err)
	}
	invalid, err := PlanOperationalImport(emptyCtx, emptyDB, emptyProject, missing.ManifestBytes, missing.RecordsBytes)
	if err != nil || invalid.ApplyAllowed || invalid.Counts[PlanInvalid] != 1 || !strings.Contains(invalid.Items[0].Reason, "missing work item") {
		t.Fatalf("missing work item dependency was not rejected: %#v err=%v", invalid, err)
	}
	if got := reviewTableTotal(t, emptyDB); got != 0 {
		t.Fatalf("invalid review dependency wrote %d rows", got)
	}
}

func appendPortableReviewTool(t *testing.T, store activity.Store, projectID, id, sessionID, client, command, outcome, signature string, at time.Time) {
	t.Helper()
	payload := map[string]any{"tool": "Bash", "command": command}
	if signature != "" {
		payload["error_signature"] = signature
	}
	eventType := "posttooluse"
	if outcome == "failure" {
		eventType = "posttoolusefailure"
	}
	err := store.Append(t.Context(), activity.Event{ID: id, ProjectID: projectID, SessionID: sessionID, TaskID: "portable-review", Client: client, EventType: eventType, Category: "tool", ActorType: "cloud_model", Outcome: outcome, Payload: payload, OccurredAt: at, ReceivedAt: at, SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
}

func reviewTableTotal(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }) int {
	t.Helper()
	total := 0
	for _, table := range []string{"work_items", "work_item_tags", "work_item_dispositions", "work_item_links", "portable_import_receipts"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		total += count
	}
	return total
}
