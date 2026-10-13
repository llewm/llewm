package portable

import (
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/pin"
	"github.com/llewm/llewm/components/continuity/internal/resource"
)

func TestResourcesRoundTrip(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "resources")
	lineage := "lineage:3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	service := resource.Service{DB: db, Now: func() time.Time { recorded = recorded.Add(time.Minute); return recorded }}
	box := resource.Resource{ProjectID: projectID, ID: "test-box", Kind: "host", Title: "Test box", Summary: "Joined " + projectID, Attributes: map[string]string{"ssh": "flux@192.168.98.198"}, Creator: "owner"}
	if _, err := service.Record(ctx, box); err != nil {
		t.Fatal(err)
	}
	box.Attributes["auth"] = "plaintext credential fallback"
	if _, err := service.Record(ctx, box); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Record(ctx, resource.Resource{ProjectID: projectID, ID: "forgejo", Kind: "service", Title: "Forgejo", Creator: "agent", Model: "model-x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddNote(ctx, resource.Note{ProjectID: projectID, ResourceID: "test-box", Text: "PATH lacks ~/.local/bin", Authority: "machine_observed", Creator: "agent"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if counts := countSpecs(loaded.Records); counts["resource"] != 3 || counts["resource_note"] != 1 {
		t.Fatalf("counts: %#v", counts)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:resources", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle.RecordsBytes), projectID) {
		t.Fatal("resource export leaked the source project ID")
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "resources-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	detail, err := (resource.Service{DB: targetDB}).Show(targetCtx, targetProject, "test-box", 0, 0)
	if err != nil || detail.Resource.Revision != 2 || detail.Resource.Attributes["auth"] != "plaintext credential fallback" || len(detail.Notes) != 1 {
		t.Fatalf("imported resource: %#v %v", detail, err)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:resources-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(3 * time.Hour), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.RecordsBytes) != string(reexport.RecordsBytes) {
		t.Fatal("resource clone re-export changed semantic records")
	}

	// A note alone, without its resource, is refused.
	var noteOnly []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "resource_note" {
			noteOnly = append(noteOnly, spec)
		}
	}
	lone, err := BuildBundle(BundleSpec{ExportID: "export:note-only", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, noteOnly)
	if err != nil {
		t.Fatal(err)
	}
	emptyCtx, emptyDB, emptyProject := operationalProject(t, "resources-empty")
	if _, err := (Service{DB: emptyDB}).Initialize(emptyCtx, emptyProject, lineage); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyOperationalImport(emptyCtx, emptyDB, emptyProject, lone.ManifestBytes, lone.RecordsBytes, recorded.Add(2*time.Hour)); err == nil && applied.Receipt != nil {
		t.Fatalf("note without its resource was applied: %#v", applied)
	}
}

func TestPinsRoundTripWithoutTheirTargets(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "pins")
	lineage := "lineage:4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b4b"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { recorded = recorded.Add(time.Minute); return recorded }
	if _, err := (resource.Service{DB: db, Now: now}).Record(ctx, resource.Resource{ProjectID: projectID, ID: "box", Kind: "host", Title: "Test box", Creator: "owner"}); err != nil {
		t.Fatal(err)
	}
	pins := pin.Service{DB: db, Now: now}
	for _, action := range []string{"pin", "unpin", "pin"} {
		if _, err := pins.Set(ctx, pin.Pin{ProjectID: projectID, TargetKind: "resource", TargetID: "box", ScopeKind: "task", ScopeID: "task-a", Action: action, Creator: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	// Only the pins travel: the target resource is left behind on purpose.
	var pinSpecs []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "pin" {
			pinSpecs = append(pinSpecs, spec)
		}
	}
	if len(pinSpecs) != 3 {
		t.Fatalf("pin specs: %d", len(pinSpecs))
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:pins", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, pinSpecs)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "pins-target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != 3 {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	lines, err := pin.ForTask(targetCtx, targetDB, targetProject, "task-a")
	if err != nil || len(lines) != 1 || lines[0].Title != "resource:box" {
		t.Fatalf("imported pins before their target: %#v %v", lines, err)
	}
}
