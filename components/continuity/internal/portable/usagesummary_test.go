package portable

import (
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/usage"
)

func TestUsageSummariesTravelAndJoinReports(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "usage")
	lineage := "lineage:7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'claude-code','done','r1','claude-opus-5-5','` + old + `',10,1000,1000,0,50,0,0)`,
		`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'claude-code','live','r2','claude-opus-5-5','` + recent + `',10,1000,1000,0,50,0,0)`,
		`INSERT INTO usage_tool_calls(project_id,client,session_id,call_id,tool,occurred_at) VALUES(?,'claude-code','done','t1','Bash','` + old + `')`,
		`INSERT INTO usage_tool_results(project_id,client,session_id,call_id,result_bytes,is_error,occurred_at) VALUES(?,'claude-code','done','t1',800,0,'` + old + `')`,
	} {
		if _, err := db.ExecContext(ctx, statement, projectID); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var specs []EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "usage_session_summary" {
			specs = append(specs, spec)
		}
	}
	// Only the idle session is finished; the live one waits.
	if len(specs) != 1 {
		t.Fatalf("usage summary specs: %d", len(specs))
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:usage", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: time.Now().UTC(), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, specs)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "usage-target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, time.Now().UTC())
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != 1 {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	stored, err := usage.StoredSummaries(targetCtx, targetDB, targetProject)
	if err != nil || len(stored) != 1 || stored[0].SchemaVersion != 2 {
		t.Fatalf("stored schema-2 summary: %+v %v", stored, err)
	}
	legacy := stored[0]
	legacy.SchemaVersion = 1
	legacy.Overhead = 999999
	legacy.ID = usage.SummaryID(legacy)
	if err := usage.InsertSummary(targetCtx, targetDB, targetProject, legacy); err != nil {
		t.Fatal(err)
	}
	report, err := usage.Reporter{DB: targetDB, ProjectID: targetProject}.Report(targetCtx, usage.Options{By: "family"})
	if err != nil || report.RemoteSessions != 1 || report.Total.Requests != 1 || report.Total.CacheWriteLong != 1000 || len(report.Tools) != 1 || report.Tools[0].ResultBytes != 800 || len(report.Overhead) != 1 || report.Overhead[0].Median != 1010 {
		t.Fatalf("report with a remote session: %+v %v", report, err)
	}
	// The source does not count its own summary twice.
	own, err := usage.Reporter{DB: db, ProjectID: projectID}.Report(ctx, usage.Options{})
	if err != nil || own.RemoteSessions != 0 || own.Total.Requests != 2 {
		t.Fatalf("source report: %+v %v", own, err)
	}
}
