package portable

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/coverage"
)

func TestCoverageCellsRoundTripWithTheirInstance(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "coverage")
	lineage := "lineage:5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, coverage.DefaultRequirementsPath), []byte("<!-- continuity:requirement session-start -->\n## Session start\n\nBootstrap at start.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { recorded = recorded.Add(time.Minute); return recorded }
	cells := coverage.Service{DB: db, Root: root, Now: now}
	for _, status := range []string{"partial", "supported"} {
		if _, err := cells.Record(ctx, "", coverage.Cell{ProjectID: projectID, RequirementID: "session-start", Client: "codex", Platform: "linux", ClientVersion: "1.5.0", Status: status, Verification: "observed", Refs: []string{"commit:abc"}, Creator: "owner"}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "coverage_cell" {
			specs = append(specs, spec)
		}
	}
	if len(specs) != 2 {
		t.Fatalf("coverage cell specs: %d", len(specs))
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:coverage", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, specs)
	if err != nil {
		t.Fatal(err)
	}
	// The target checkout has no requirements file yet: cells import anyway.
	targetCtx, targetDB, targetProject := operationalProject(t, "coverage-target")
	target, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != 2 {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	current, err := (coverage.Service{DB: targetDB}).Current(targetCtx, targetProject)
	if err != nil || len(current) != 1 || current[0].Status != "supported" || current[0].InstanceID != identity.InstanceID || current[0].InstanceID == target.InstanceID {
		t.Fatalf("imported current cells: %#v %v", current, err)
	}
	again, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(3*time.Hour))
	if err != nil || again.Receipt == nil || again.Receipt.AppendedRecords != 0 || again.Receipt.AlreadyPresentRecords != 2 {
		t.Fatalf("reapply: %#v %v", again, err)
	}
}
