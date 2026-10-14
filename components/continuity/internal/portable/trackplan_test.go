package portable

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func TestTrackPlanRecordsRoundTripReplayCollisionAndDependencies(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "track-plan-source")
	lineage := "lineage:44444444444444444444444444444444"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	sourceRoot := strings.TrimSuffix(sourceDBPath(t, sourceDB), "/continuity.db")
	items := workitem.Service{DB: sourceDB, Now: func() time.Time { return recorded.Add(time.Minute) }}
	source, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "item-portable", ProjectID: sourceProject, Kind: "task", Title: "Carry " + sourceRoot, Body: "Avoid rediscovery", Creator: "codex", Model: "model-a", TaskID: "portable-records", RoadmapNodeID: "continuity-track-portability"}})
	if err != nil {
		t.Fatal(err)
	}
	service := trackplan.Service{DB: sourceDB}
	service.Now = func() time.Time { return recorded.Add(4 * time.Minute) }
	first, err := service.AppendPlan(ctx, trackplan.Plan{ProjectID: sourceProject, ID: "portable-track-plan", RoadmapNodeID: "continuity-track-portability", TaskID: "portable-records", Title: "Portable track records", Summary: "Transfer reviewed state", Status: "accepted", Creator: "human-review", WorkItemIDs: []string{source.ID}, Items: []trackplan.Item{{ID: "codec", Position: 0, Title: "Add records", Detail: "Implement below " + sourceRoot, Status: "active", WorkItemIDs: []string{source.ID}}, {ID: "qualification", Position: 1, Title: "Qualify", Status: "pending", DependsOn: []string{"codec"}, EvidenceRefs: []string{"commit:0123456789abcdef0123456789abcdef01234567"}}}})
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return recorded.Add(5 * time.Minute) }
	second := first
	second.Revision = 0
	second.Status = "active"
	second.BaseRevision = first.Revision
	second.Creator, second.Model = "agent", "fixture-model"
	second.Items = append([]trackplan.Item{}, first.Items...)
	second.Items[0].Status = "completed"
	second.Items[0].ProgressNote = "Codec checks passed"
	second.Items[0].EvidenceRefs = []string{"commit:0123456789abcdef0123456789abcdef01234567"}
	second.Items[1].Status = "ready"
	if _, err := service.AppendPlan(ctx, second); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:track-plan", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(6 * time.Minute), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	for _, recordType := range []string{"work_item", "track_plan"} {
		if bundle.Manifest.RecordCounts[recordType] == 0 {
			t.Fatalf("missing %s from portable bundle: %#v", recordType, bundle.Manifest.RecordCounts)
		}
	}
	if strings.Contains(string(bundle.RecordsBytes), sourceRoot) || !strings.Contains(string(bundle.RecordsBytes), "[PROJECT_ROOT]") {
		t.Fatal("track-plan export did not replace the source root")
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "track-plan-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("unexpected track-plan apply: %#v", applied)
	}
	history, err := (trackplan.Service{DB: targetDB}).PlanHistory(targetCtx, targetProject, first.ID)
	if err != nil || len(history) != 2 || history[1].Items[0].Status != "completed" || history[1].Items[1].DependsOn[0] != "codec" || len(history[0].WorkItemIDs) != 1 || history[0].Items[0].WorkItemIDs[0] != source.ID {
		t.Fatalf("imported plan history differs: %#v %v", history, err)
	}
	if history[1].Model != "fixture-model" || history[1].Change.Kind != "progress" || history[1].Change.BaselineRevision != first.Revision || history[1].Items[0].ProgressNote != "Codec checks passed" {
		t.Fatal("autonomous progress provenance did not round trip")
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:track-plan-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(8 * time.Minute), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundle.RecordsBytes, reexport.RecordsBytes) || bundle.Manifest.RecordSetSHA256 != reexport.Manifest.RecordSetSHA256 {
		t.Fatal("track-plan records changed across portable round trip")
	}
	replayed, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(9*time.Minute))
	if err != nil || replayed.Receipt == nil || replayed.Receipt.AppendedRecords != 0 || replayed.Receipt.AlreadyPresentRecords != len(bundle.Records) {
		t.Fatalf("track-plan replay was not idempotent: %#v %v", replayed, err)
	}

	collisionCtx, collisionDB, collisionProject := operationalProject(t, "track-plan-collision")
	if _, err := (Service{DB: collisionDB}).Initialize(collisionCtx, collisionProject, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (workitem.Service{DB: collisionDB, Now: func() time.Time { return source.RecordedAt }}).Capture(collisionCtx, workitem.Capture{Item: workitem.Item{ID: source.ID, ProjectID: collisionProject, Kind: "bug", Title: "Different", Body: "Collision", Creator: "codex"}}); err != nil {
		t.Fatal(err)
	}
	collision, err := PlanOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || collision.ApplyAllowed || collision.Counts[PlanCollision] != 1 {
		t.Fatalf("work item collision was not refused: %#v %v", collision, err)
	}

	var planSpec EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "track_plan" && spec.RecordRevision == 1 {
			planSpec = spec
			break
		}
	}
	missingBundle, err := BuildBundle(BundleSpec{ExportID: "export:missing-work-item", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(10 * time.Minute), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, []EnvelopeSpec{planSpec})
	if err != nil {
		t.Fatal(err)
	}
	dependencyCtx, dependencyDB, dependencyProject := operationalProject(t, "track-plan-dependency")
	if _, err := (Service{DB: dependencyDB}).Initialize(dependencyCtx, dependencyProject, lineage); err != nil {
		t.Fatal(err)
	}
	if result, err := ApplyOperationalImport(dependencyCtx, dependencyDB, dependencyProject, missingBundle.ManifestBytes, missingBundle.RecordsBytes, recorded.Add(11*time.Minute)); err == nil || result.Receipt != nil || tableCount(t, dependencyDB, "track_plan_revisions") != 0 {
		t.Fatalf("missing work item dependency was not rejected atomically: %#v %v", result, err)
	}
}

func TestLegacyTrackPlanPayloadRetainsItsHash(t *testing.T) {
	ctx, db, project := operationalProject(t, "legacy-plan")
	value := trackplan.Plan{ProjectID: project, ID: "legacy", Revision: 1, RoadmapNodeID: "track", Title: "Old intent", Summary: "Retain original", Status: "active", Creator: "person", Authority: "caller_asserted", RecordedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), SchemaVersion: 1, Items: []trackplan.Item{{ID: "one", Title: "One", Detail: "Original scope", Status: "active", EvidenceRefs: []string{}, WorkItemIDs: []string{}}}}
	// A pre-075 row has no change metadata or separate note/criteria values.
	_, err := db.ExecContext(ctx, `INSERT INTO track_plan_revisions(project_id,id,revision,roadmap_node_id,title,summary,status,creator,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, project, value.ID, 1, value.RoadmapNodeID, value.Title, value.Summary, value.Status, value.Creator, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO track_plan_items(project_id,plan_id,plan_revision,id,position,title,detail,status,evidence_refs_json,work_item_refs_json) VALUES(?,?,1,'one',0,'One','Original scope','active','[]','[]')`, project, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := trackPlanSpec("lineage:55555555555555555555555555555555", project, "/project", value)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTrackPlan(ctx, db, project, value.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	current, err := trackPlanSpec("lineage:55555555555555555555555555555555", project, "/project", loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original.Payload, current.Payload) {
		t.Fatal("legacy plan payload changed")
	}
}
