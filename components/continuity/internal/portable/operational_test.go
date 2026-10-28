package portable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

const operationalCommit = "0123456789abcdef0123456789abcdef01234567"

func TestCachedInvocationTextMatchesRedaction(t *testing.T) {
	root := "/home/example/project"
	value := attribution.Invocation{ID: "invocation", ActorType: "agent", Client: "codex", ClientVersion: "1", Provider: "example", Model: "token=secret-value", IdentitySource: "caller_asserted", OccurredAt: time.Now().UTC(), ReceivedAt: time.Now().UTC(), SchemaVersion: 1, EffortRequested: &attribution.Effort{Raw: root + "/config", Normalized: "high", Source: "caller", Authority: "caller_asserted"}}
	uncached := func(s string) string { return portableText(s, root) }
	cached := cachedPortableText(root)
	for i := 0; i < 2; i++ {
		want, err := invocationSpec("lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", uncached, value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := invocationSpec("lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", cached, value)
		if err != nil {
			t.Fatal(err)
		}
		if string(want.Payload) != string(got.Payload) || string(want.Index) != string(got.Index) || strings.Contains(string(got.Payload), "secret-value") || strings.Contains(string(got.Payload), root) {
			t.Fatalf("cached invocation differs or leaked sensitive text: %s", got.Payload)
		}
	}
}

func BenchmarkInvocationRepeatedMetadata(b *testing.B) {
	root := "/home/example/project"
	value := attribution.Invocation{ID: "invocation", ActorType: "agent", Client: "codex", ClientVersion: "1", Provider: "example", Model: "model", IdentitySource: "caller_asserted", OccurredAt: time.Unix(1, 0), ReceivedAt: time.Unix(1, 0), SchemaVersion: 1, EffortRequested: &attribution.Effort{Raw: "high", Normalized: "high", Source: "caller", Authority: "caller_asserted"}}
	for _, cached := range []bool{false, true} {
		name := "uncached"
		text := func(s string) string { return portableText(s, root) }
		if cached {
			name, text = "cached", cachedPortableText(root)
		}
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := invocationSpec("lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", text, value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func operationalProject(t testing.TB, name string) (context.Context, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), name)
	db, err := database.Initialize(ctx, filepath.Join(root, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, name)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, db, projectID
}

func TestImportPlanRechecksAfterConcurrentCommit(t *testing.T) {
	ctx, source, sourceProject := operationalProject(t, "plan-source")
	lineage := "lineage:99999999999999999999999999999999"
	if _, err := (Service{DB: source}).Initialize(ctx, sourceProject, lineage); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := (attribution.Service{DB: source}).AppendInvocation(ctx, attribution.Invocation{ID: "shared", ProjectID: sourceProject, ActorType: "agent", Model: "original", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := (worknarrative.Service{DB: source, Now: func() time.Time { return at }}).Append(ctx, worknarrative.Record{ID: "plan-narrative", ProjectID: sourceProject, Title: "Plan", Trigger: "manual", Creator: "test", CreatorInvocationID: "shared", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Evidence", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, source, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:plan-proof", LineageID: lineage, SourceInstanceID: loaded.Identity.InstanceID, CreatedAt: at, SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("d", 64)}, Omissions: []Omission{}}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, target, targetProject := operationalProject(t, "plan-target")
	if _, err := (Service{DB: target}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	conn, err := target.Conn(targetCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version int64
	if err := conn.QueryRowContext(targetCtx, `PRAGMA data_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	plan, err := planOperationalImport(targetCtx, conn, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != 2 {
		t.Fatalf("initial plan: %+v %v", plan, err)
	}
	// An unchanged connection may reuse the reviewed plan and append the same
	// records as an ordinary apply to a separate project.
	cleanCtx, clean, cleanProject := operationalProject(t, "plan-clean")
	if _, err := (Service{DB: clean}).Initialize(cleanCtx, cleanProject, lineage); err != nil {
		t.Fatal(err)
	}
	cleanConn, err := clean.Conn(cleanCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanConn.Close()
	var cleanVersion int64
	if err := cleanConn.QueryRowContext(cleanCtx, `PRAGMA data_version`).Scan(&cleanVersion); err != nil {
		t.Fatal(err)
	}
	cleanPlan, err := planOperationalImport(cleanCtx, cleanConn, cleanProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil {
		t.Fatal(err)
	}
	// The marker is informational and proves this particular plan, rather
	// than a silent second full planning pass, was used on the unchanged path.
	cleanPlan.Items[0].Reason = "verified-preflight-plan"
	cleanResult, err := applyOperationalImportWithPlan(cleanCtx, cleanConn, cleanProject, bundle.ManifestBytes, bundle.RecordsBytes, at, nil, newReusableImportPlan(cleanPlan, cleanVersion, cleanProject, bundle.ManifestBytes, bundle.RecordsBytes))
	if err != nil || cleanResult.Receipt == nil || cleanResult.Receipt.AppendedRecords != 2 {
		t.Fatalf("unchanged plan apply: %+v %v", cleanResult, err)
	}
	if cleanResult.Plan.Items[0].Reason != "verified-preflight-plan" {
		t.Fatal("unchanged preflight was not reused")
	}
	refCtx, reference, refProject := operationalProject(t, "plan-reference")
	if _, err := (Service{DB: reference}).Initialize(refCtx, refProject, lineage); err != nil {
		t.Fatal(err)
	}
	refResult, err := ApplyOperationalImport(refCtx, reference, refProject, bundle.ManifestBytes, bundle.RecordsBytes, at)
	if err != nil || refResult.Receipt == nil || refResult.Receipt.AppendedRecords != cleanResult.Receipt.AppendedRecords {
		t.Fatalf("reference apply differs: %+v %v", refResult, err)
	}
	cleanExport, err := EncodeMemoryExport(cleanCtx, clean, cleanProject)
	if err != nil {
		t.Fatal(err)
	}
	refExport, err := EncodeMemoryExport(refCtx, reference, refProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanExport.Envelopes) != len(refExport.Envelopes) {
		t.Fatalf("imported record counts differ: %d %d", len(cleanExport.Envelopes), len(refExport.Envelopes))
	}
	for i := range cleanExport.Envelopes {
		if !bytes.Equal(cleanExport.Envelopes[i].Bytes, refExport.Envelopes[i].Bytes) {
			t.Fatalf("imported record %d differs", i)
		}
	}
	if _, err := (attribution.Service{DB: target}).AppendInvocation(targetCtx, attribution.Invocation{ID: "shared", ProjectID: targetProject, ActorType: "agent", Model: "conflicting", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	result, err := applyOperationalImportWithPlan(targetCtx, conn, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, at, nil, newReusableImportPlan(plan, version, targetProject, bundle.ManifestBytes, bundle.RecordsBytes))
	if err == nil || result.Plan.ApplyAllowed || result.Plan.Counts[PlanCollision] != 1 || result.Receipt != nil {
		t.Fatalf("stale plan applied: %+v %v", result, err)
	}
}

func TestOperationalRecordsAndDryRunImportPlan(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "source")
	lineage := "lineage:99999999999999999999999999999999"
	if _, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage); err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)
	invocation, err := (attribution.Service{DB: sourceDB}).AppendInvocation(ctx, attribution.Invocation{
		ID: "invocation-source", ProjectID: sourceProject, SessionID: "session-a",
		ActorType: "agent", Client: "codex", Provider: "openai", Model: "model-a",
		IdentitySource: "client_hook_claim", OccurredAt: recorded, ReceivedAt: recorded,
		EffortRequested: &attribution.Effort{Raw: "HIGH", Normalized: "high", Source: "request_metadata", Authority: "client_hook_claim"},
		EffortEffective: &attribution.Effort{Raw: "provider-high", Source: "provider_response", Authority: "provider_reported"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Dir(sourceDBPath(t, sourceDB))
	created, err := (worknarrative.Service{DB: sourceDB, Now: func() time.Time { return recorded }}).Append(ctx, worknarrative.Record{
		ID: "narrative-source", ProjectID: sourceProject, Title: "Portable mapping", Trigger: "manual",
		Claims:  []worknarrative.Claim{{Kind: "outcome", Text: "Mapped the real record at " + sourceRoot + ".", Authority: "caller_asserted"}},
		Anchors: []worknarrative.Anchor{{Kind: "task", Target: "portable-memory", Authority: "caller_asserted"}},
		Creator: "codex", CreatorInvocationID: invocation.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Identity.LineageID != lineage || len(loaded.Records) != 2 || len(loaded.MissingReferences) != 0 {
		t.Fatalf("unexpected operational mapping: %#v", loaded)
	}
	bundle, err := BuildBundle(BundleSpec{
		ExportID: "export:operational", LineageID: lineage, SourceInstanceID: loaded.Identity.InstanceID,
		CreatedAt: recorded, SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1",
		RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("d", 64)}, Omissions: []Omission{},
	}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(bundle.RecordsBytes)
	if strings.Contains(serialized, sourceProject) || strings.Contains(serialized, sourceRoot) || !strings.Contains(serialized, "[PROJECT_ROOT]") {
		t.Fatal("portable records leaked a local project identity or path")
	}
	if !strings.Contains(serialized, `"creator_invocation_id":"`+invocation.ID+`"`) || !strings.Contains(serialized, `"effort_requested":{"authority":"client_hook_claim","normalized":"high","raw":"HIGH","source":"request_metadata"}`) {
		t.Fatalf("portable provenance or effort was lost: %s", serialized)
	}
	same, err := PlanOperationalImport(ctx, sourceDB, sourceProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !same.ApplyAllowed || same.Counts[PlanAlreadyPresent] != 2 || same.WritesPerformed != 0 {
		t.Fatalf("same-project plan was not idempotent: %#v %v", same, err)
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	empty, err := PlanOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !empty.ApplyAllowed || empty.Counts[PlanAppend] != 2 || empty.WritesPerformed != 0 {
		t.Fatalf("empty-target plan was not append-only: %#v %v", empty, err)
	}
	if _, err := (attribution.Service{DB: targetDB}).AppendInvocation(targetCtx, attribution.Invocation{ID: invocation.ID, ProjectID: targetProject, ActorType: "agent", Model: "different-model", IdentitySource: "caller_asserted", OccurredAt: recorded, ReceivedAt: recorded}); err != nil {
		t.Fatal(err)
	}
	collision, err := PlanOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || collision.ApplyAllowed || collision.Counts[PlanCollision] != 1 || collision.Counts[PlanAppend] != 1 || collision.WritesPerformed != 0 {
		t.Fatalf("collision plan was not visible and write-free: %#v %v", collision, err)
	}
	var narrativeCount int
	if err := targetDB.QueryRowContext(targetCtx, `SELECT COUNT(*) FROM work_narratives`).Scan(&narrativeCount); err != nil || narrativeCount != 0 {
		t.Fatalf("dry-run plan wrote narratives: count=%d err=%v", narrativeCount, err)
	}
	if created.CreatorInvocationID != invocation.ID {
		t.Fatalf("source narrative link changed: %#v", created)
	}
}

func sourceDBPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOperationalImportPlanRejectsLineageMismatchAndInvalidRecords(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "project")
	lineage := "lineage:77777777777777777777777777777777"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 18, 16, 0, 0, 0, time.UTC)
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:valid", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("e", 64)}, Omissions: []Omission{}}, []EnvelopeSpec{{LineageID: lineage, RecordType: "work_item", RecordSchemaVersion: 1, RecordID: "item", RecordedAt: recorded, Authority: "caller_asserted", Index: json.RawMessage(`{"status":"idea","title":"Later"}`), Payload: json.RawMessage(`{"body":"Review later","creator":"human","kind":"idea","operational_schema_version":1,"tags":[],"title":"Later"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != 1 {
		t.Fatalf("valid family was not classified for append: %#v %v", plan, err)
	}
	tampered := append([]byte(nil), bundle.RecordsBytes...)
	tampered[len(tampered)-3] ^= 1
	invalid, err := PlanOperationalImport(ctx, db, projectID, bundle.ManifestBytes, tampered)
	if err != nil || invalid.ApplyAllowed || invalid.Counts[PlanInvalid] != 1 || invalid.WritesPerformed != 0 || len(invalid.Items) != 1 || invalid.Items[0].Reason == "" {
		t.Fatalf("invalid bundle was not classified without writes: %#v %v", invalid, err)
	}
	semantic, err := BuildBundle(BundleSpec{ExportID: "export:semantic-invalid", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: "016_portable_import_receipts.sql", SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("f", 64)}, Omissions: []Omission{}}, []EnvelopeSpec{{LineageID: lineage, RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: "empty", RecordedAt: recorded, Authority: "mixed", Payload: json.RawMessage(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	semanticInvalid, err := PlanOperationalImport(ctx, db, projectID, semantic.ManifestBytes, semantic.RecordsBytes)
	if err != nil || semanticInvalid.Counts[PlanInvalid] != 1 || semanticInvalid.ApplyAllowed {
		t.Fatalf("semantically invalid record was not classified invalid: %#v %v", semanticInvalid, err)
	}

	otherCtx, otherDB, otherProject := operationalProject(t, "other")
	if _, err := (Service{DB: otherDB}).Initialize(otherCtx, otherProject, "lineage:88888888888888888888888888888888"); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOperationalImport(otherCtx, otherDB, otherProject, bundle.ManifestBytes, bundle.RecordsBytes); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("lineage mismatch was accepted: %v", err)
	}
}

func TestWorkNarrativesRoundTripWithSupersessionAndMultipleCheckpoints(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "work-narrative-source")
	lineage := "lineage:55555555555555555555555555555555"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)
	service := worknarrative.Service{DB: sourceDB, Now: func() time.Time { return recorded }}
	first, err := service.Append(ctx, worknarrative.Record{ID: "episode-a", ProjectID: sourceProject, Title: "Bounded episode", Trigger: "meaningful_checkpoint", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Captured non-commit work", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "checkpoint", Target: "checkpoint-a", Authority: "caller_asserted"}, {Kind: "checkpoint", Target: "checkpoint-b", Authority: "caller_asserted"}}})
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return recorded.Add(time.Minute) }
	if _, err = service.Append(ctx, worknarrative.Record{ID: "episode-b", ProjectID: sourceProject, Title: "Corrected episode", Trigger: "manual", Creator: "human", SupersedesID: first.ID, Claims: []worknarrative.Claim{{Kind: "decision", Text: "Use the correction", Authority: "user_reported"}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:work-narratives", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(2 * time.Minute), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "work-narrative-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(3*time.Minute))
	if err != nil || applied.Plan.Counts[PlanAppend] != 3 {
		t.Fatalf("work narrative family was not applied: %#v %v", applied, err)
	}
	shown, err := (worknarrative.Service{DB: targetDB}).Show(targetCtx, targetProject, first.ID, 4096)
	if err != nil || shown == nil || shown.Record.State != "superseded" || len(shown.Record.Anchors) != 2 {
		t.Fatalf("imported narrative differs: %#v %v", shown, err)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:work-narrative-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(4 * time.Minute), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.RecordsBytes) != string(reexport.RecordsBytes) {
		t.Fatalf("work narrative records changed across round trip\nsource: %s\nclone: %s", bundle.RecordsBytes, reexport.RecordsBytes)
	}
}

func TestNarrativeEnvelopesStayStableWhenSuperseded(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "stable-narratives")
	if _, err := (Service{DB: db}).Initialize(ctx, projectID, "lineage:44444444444444444444444444444444"); err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	workService := worknarrative.Service{DB: db, Now: func() time.Time { return recorded }}
	if _, err := workService.Append(ctx, worknarrative.Record{ID: "work-a", ProjectID: projectID, Title: "First", Trigger: "manual", Creator: "codex", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "First account", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	envelopes := func() map[string]string {
		t.Helper()
		loaded, err := LoadOperationalRecords(ctx, db, projectID)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]string{}
		for _, spec := range loaded.Records {
			encoded, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			result[spec.RecordType+"/"+spec.RecordID] = string(encoded)
		}
		return result
	}
	before := envelopes()
	workService.Now = func() time.Time { return recorded.Add(time.Minute) }
	if _, err := workService.Append(ctx, worknarrative.Record{ID: "work-b", ProjectID: projectID, Title: "Second", Trigger: "manual", Creator: "codex", SupersedesID: "work-a", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Corrected account", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	after := envelopes()
	// Sync publishes each envelope once, so a successor must not re-encode its predecessor.
	for _, key := range []string{"work_narrative/work-a"} {
		if before[key] == "" || before[key] != after[key] {
			t.Fatalf("%s changed when superseded\nbefore: %s\nafter:  %s", key, before[key], after[key])
		}
	}
}

func TestPortableTextRedactsEveryRecordedProjectLocation(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "moved")
	var current string
	if err := db.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, projectID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	former, nested := "/old/checkout", current+"/nested-old"
	for _, path := range []string{former, nested} {
		if _, err := db.ExecContext(ctx, `INSERT INTO repository_locations(project_id, repository_id, path, root_commit, source, observed_at, schema_version)
            SELECT ?, id, ?, NULL, 'relocated', '2026-09-27T00:00:00Z', 1 FROM repositories WHERE project_id=?`, projectID, path, projectID); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := projectRootFrom(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	got := portableText("edited "+former+"/a.go, "+nested+"/b.go, and "+current+"/c.go", roots)
	if got != "edited [PROJECT_ROOT]/a.go, [PROJECT_ROOT]/b.go, and [PROJECT_ROOT]/c.go" {
		t.Fatalf("portableText = %q", got)
	}
}

func TestWorkspaceSyncWatermarksRoundTrip(t *testing.T) {
	ctx, db, projectID := operationalProject(t, "workspace")
	lineage := "lineage:0123456789abcdef0123456789abcdef"
	identity, err := (Service{DB: db}).Initialize(ctx, projectID, lineage)
	if err != nil {
		t.Fatal(err)
	}
	const apiCommit, daemonCommit = "0123456789abcdef0123456789abcdef01234567", "89abcdef0123456789abcdef0123456789abcdef"
	head := "api@" + apiCommit + ",daemon@" + daemonCommit
	for _, statement := range []string{
		`INSERT INTO roadmap_nodes(project_id,node_id,revision,title,status,priority,source_path,authority,recorded_at,schema_version) VALUES(?, 'fleet.wire', 1, 'Wire', 'active', 10, 'api/README.md', 'caller_asserted', '2026-09-27T00:00:00Z', 1)`,
		`INSERT INTO roadmap_sync_runs(id,project_id,git_commit,model,reviewed_nodes_json,reviewed_files_json,authority,git_authority,completed_at,schema_version) VALUES('sync-ws', ?, '` + head + `', 'model-a', '["fleet.wire"]', '["api/README.md"]', 'caller_asserted', 'machine_observed', '2026-09-27T00:00:00Z', 1)`,
	} {
		if _, err := db.ExecContext(ctx, statement, projectID); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var commits []string
	for _, record := range loaded.Records {
		if record.RecordType == "roadmap_sync" {
			for _, ref := range record.Refs {
				if ref.Kind == "commit" {
					commits = append(commits, ref.ID)
				}
			}
		}
	}
	if strings.Join(commits, ",") != apiCommit+","+daemonCommit {
		t.Fatalf("workspace sync commit refs = %v", commits)
	}
	created := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:workspace", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: created, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	targetCtx, targetDB, targetProject := operationalProject(t, "workspace-target")
	if _, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, created.Add(time.Minute)); err != nil || applied.Receipt == nil {
		t.Fatalf("apply: %#v %v", applied, err)
	}
	var imported string
	if err := targetDB.QueryRowContext(targetCtx, `SELECT git_commit FROM roadmap_sync_runs WHERE id='sync-ws'`).Scan(&imported); err != nil || imported != head {
		t.Fatalf("imported watermark = %q %v", imported, err)
	}
}

func TestValidSyncHead(t *testing.T) {
	const a, b = "0123456789abcdef0123456789abcdef01234567", "89abcdef0123456789abcdef0123456789abcdef"
	for value, want := range map[string]bool{
		a:                                  true,
		"api@" + a + ",daemon@" + b:        true,
		"svc/api@" + a:                     true,
		"daemon@" + b + ",api@" + a:        false, // unsorted
		"api@" + a + ",api@" + b:           false, // duplicate member
		"../api@" + a:                      false,
		"api@" + a[:12]:                    false,
		"api@" + a + ",":                   false,
		"":                                 false,
		"api@" + strings.ToUpper(a):        false,
		"api@" + a + ",daemon@" + b + "@x": false,
	} {
		if got := validSyncHead(value); got != want {
			t.Fatalf("validSyncHead(%q) = %t, want %t", value, got, want)
		}
	}
}
