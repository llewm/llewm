package portable

import (
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestReviewedReleaseRoundTrips(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "release-source")
	lineage := "lineage:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.Exec(`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version,kind,evidence_refs_json) VALUES(?, 'done',1,'Done','completed',10,'docs/roadmap.md','caller_asserted','2026-01-01T00:00:00Z',1,'work_item','[]')`, sourceProject); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: sourceDB}).Append(ctx, worknarrative.Record{ID: "release-narrative", ProjectID: sourceProject, Title: "Release", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Added release records.", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "roadmap_node", Target: "done", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	created, err := (release.Service{DB: sourceDB}).Append(ctx, release.Record{ProjectID: sourceProject, ID: "unreleased", Version: "Unreleased", Title: "Draft", Status: "draft", Entries: []release.Entry{{Category: "feature", Wording: "Portable releases.", NodeID: "done", NodeRevision: 1, NarrativeIDs: []string{"release-narrative"}}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	if countSpecs(loaded.Records)["roadmap_release"] != 1 {
		t.Fatalf("missing release record: %#v", countSpecs(loaded.Records))
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:releases", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Now().UTC(), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "release-target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt == nil {
		t.Fatal("missing import receipt")
	}
	if got := tableCount(t, targetDB, "roadmap_releases"); got != 1 {
		t.Fatalf("release rows=%d", got)
	}
	view, err := (release.Service{DB: targetDB}).History(targetCtx, targetProject, "unreleased", 1)
	if err != nil || len(view.Releases) != 1 || view.Releases[0].ContentSHA256 != created.ContentSHA256 {
		t.Fatalf("imported view=%#v err=%v", view, err)
	}
}
