package portable

import (
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestSourceCitationsRoundTripAndRefuseMissingObservation(t *testing.T) {
	ctx, db, p := operationalProject(t, "research-source")
	lineage := "lineage:3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a"
	identity, err := (Service{DB: db}).Initialize(ctx, p, lineage)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s := worknarrative.Service{DB: db, Now: func() time.Time { return now }}
	obs := worknarrative.Record{ID: "z-source", ProjectID: p, Title: "Source", Trigger: "manual", Creator: "agent", Source: &worknarrative.Source{URL: "https://example.org/reference?version=1", AccessedAt: now.Format(time.RFC3339), Publisher: "Example", Authority: "caller_asserted"}, Claims: []worknarrative.Claim{{Kind: "note", Text: "Scope summary", Authority: "caller_asserted"}}}
	if _, err := s.Append(ctx, obs); err != nil {
		t.Fatal(err)
	}
	finding := worknarrative.Record{ID: "a-finding", ProjectID: p, Title: "Finding", Trigger: "manual", Creator: "agent", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Conclusion", Authority: "caller_asserted", Citations: []worknarrative.Citation{{Source: "narrative:z-source", Relation: "supports", Locator: "section 1"}, {Source: "narrative:z-source", Relation: "context", Locator: "section 2"}}}}}
	if _, err := s.Append(ctx, finding); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, p)
	if err != nil {
		t.Fatal(err)
	}
	build := func(id string, records []EnvelopeSpec, instance string) Bundle {
		t.Helper()
		b, err := BuildBundle(BundleSpec{ExportID: "export:" + id, LineageID: lineage, SourceInstanceID: instance, CreatedAt: now.Add(time.Hour), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, records)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := build("research", loaded.Records, identity.InstanceID)
	tctx, tdb, tp := operationalProject(t, "research-target")
	ti, err := (Service{DB: tdb}).Initialize(tctx, tp, lineage)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyOperationalImport(tctx, tdb, tp, b.ManifestBytes, b.RecordsBytes, now.Add(2*time.Hour))
	if err != nil || result.Receipt == nil {
		t.Fatalf("import: %+v %v", result, err)
	}
	shown, err := (worknarrative.Service{DB: tdb}).Show(tctx, tp, "z-source", 4096)
	if err != nil || shown.Record.Source.URL != obs.Source.URL {
		t.Fatalf("source: %+v %v", shown, err)
	}
	var count int
	if err := tdb.QueryRow(`SELECT COUNT(*) FROM work_narrative_citations WHERE project_id=?`, tp).Scan(&count); err != nil || count != 2 {
		t.Fatalf("edges=%d %v", count, err)
	}
	again, err := LoadOperationalRecords(tctx, tdb, tp)
	if err != nil {
		t.Fatal(err)
	}
	b2 := build("research-again", again.Records, ti.InstanceID)
	if string(b.RecordsBytes) != string(b2.RecordsBytes) {
		t.Fatal("research round-trip changed canonical records")
	}
	var lone []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordID == "a-finding" {
			lone = append(lone, spec)
		}
	}
	missing := build("missing-source", lone, identity.InstanceID)
	ectx, edb, ep := operationalProject(t, "research-empty")
	if _, err := (Service{DB: edb}).Initialize(ectx, ep, lineage); err != nil {
		t.Fatal(err)
	}
	r, err := ApplyOperationalImport(ectx, edb, ep, missing.ManifestBytes, missing.RecordsBytes, now.Add(2*time.Hour))
	if err == nil && r.Receipt != nil {
		t.Fatal("orphan citation imported")
	}
}
