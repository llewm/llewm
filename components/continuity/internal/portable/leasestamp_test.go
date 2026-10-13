package portable

import (
	"reflect"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
)

func TestLeaseStampsAndStaleResolutionsRoundTrip(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "stamps")
	lineage := "lineage:6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d6d"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	stamps := []lease.Stamp{
		{RecordKind: lease.StampCheckpoint, RecordID: "cp-1", ScopeKind: "task", ScopeID: "t1", State: lease.Leased, Token: 3, Holder: "claude-code", SessionID: "s", InstanceID: identity.InstanceID, RecordedAt: recorded},
		{RecordKind: lease.StampPlan, RecordID: "plan@2", ScopeKind: "track_plan", ScopeID: "plan", State: lease.Lapsed, Token: 1, Holder: "codex", InstanceID: identity.InstanceID, RecordedAt: recorded.Add(time.Second)},
	}
	for _, stamp := range stamps {
		if err := lease.InsertStamp(ctx, db, projectID, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// Unleased stamps stay on this machine.
	if err := lease.InsertStamp(ctx, db, projectID, lease.Stamp{RecordKind: lease.StampCheckpoint, RecordID: "cp-2", ScopeKind: "task", ScopeID: "t1", State: lease.Unleased, RecordedAt: recorded.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := lease.InsertStaleResolution(ctx, db, lease.StaleResolution{ProjectID: projectID, ID: "res-1", RecordKind: lease.StampCheckpoint, RecordID: "cp-0", Accepted: lease.AcceptWrite, Resolver: "owner", Authority: "user_reported", RecordedAt: recorded, SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "lease_stamp" || spec.RecordType == "stale_write_resolution" {
			specs = append(specs, spec)
		}
	}
	if len(specs) != 3 {
		t.Fatalf("lease specs: %d", len(specs))
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:stamps", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, specs)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "stamps-target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Hour))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != 3 {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	got, err := lease.Stamps(targetCtx, targetDB, targetProject)
	if err != nil || !reflect.DeepEqual(got, stamps) {
		t.Fatalf("imported stamps: %+v %v", got, err)
	}
	resolutions, err := lease.StaleResolutions(targetCtx, targetDB, targetProject)
	if err != nil || len(resolutions) != 1 || resolutions[0].Accepted != lease.AcceptWrite {
		t.Fatalf("imported resolutions: %+v %v", resolutions, err)
	}
}
