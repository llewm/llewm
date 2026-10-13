package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

type portableRoadmapRepository struct {
	head  string
	files map[string][]string
}

func TestRoadmapNodeV1DefaultsToWorkItem(t *testing.T) {
	payload := roadmapNodePayload{Title: "Legacy roadmap node", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md", OperationalSchemaVersion: roadmap.SchemaVersion}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(envelopeIndex{Title: payload.Title, Status: payload.Status})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:abababababababababababababababab", RecordType: "roadmap_node", RecordSchemaVersion: 1, RecordID: "legacy-node", RecordRevision: 1, RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Index: index, Refs: roadmapNodeRefs(payload), Payload: body})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRoadmapNode(encoded.Envelope, payload)
	if err != nil || decoded.Kind != "work_item" || len(decoded.EvidenceRefs) != 0 {
		t.Fatalf("decoded v1 node = %#v err=%v", decoded, err)
	}
}

func TestSchemaObservationReplayKeepsAcceptedRoadmapAndRealConflicts(t *testing.T) {
	ctx, db, project := operationalProject(t, "schema-replay")
	lineage := "lineage:abababababababababababababababab"
	identity, err := (Service{DB: db}).Initialize(ctx, project, lineage)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sourceDBPath(t, db))
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/roadmap.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	service := roadmap.Service{DB: db, Root: root, Repo: portableRoadmapRepository{head: operationalCommit}, Now: func() time.Time { return at }}
	if _, err := service.AppendNode(ctx, roadmap.Node{ProjectID: project, ID: "node", Title: "Accepted", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md", SourceRef: operationalCommit}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	var base EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "roadmap_node" {
			base = spec
			break
		}
	}
	if base.RecordSchemaVersion != 1 {
		t.Fatal("default node should keep its canonical v1 schema")
	}
	for _, kind := range []string{"work_item", "milestone"} {
		t.Run(kind, func(t *testing.T) {
			var payload roadmapNodePayload
			if err := json.Unmarshal(base.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload.Kind = kind
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			spec := base
			spec.RecordSchemaVersion, spec.Payload = 2, body
			bundle, err := BuildBundle(BundleSpec{ExportID: "export:schema-" + kind, LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: at, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, []EnvelopeSpec{spec})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PlanOperationalImport(ctx, db, project, bundle.ManifestBytes, bundle.RecordsBytes)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "work_item" {
				if !plan.ApplyAllowed || plan.Counts[PlanAlreadyPresent] != 1 || plan.Counts[PlanCollision] != 0 {
					t.Fatalf("equivalent schema reported as conflicting: %+v", plan)
				}
			} else if plan.ApplyAllowed || plan.Counts[PlanCollision] != 1 {
				t.Fatalf("genuine semantic change was accepted: %+v", plan)
			}
			if plan.WritesPerformed != 0 {
				t.Fatal("read-only planner wrote state")
			}
		})
	}
}

func (repository portableRoadmapRepository) Head(context.Context) (string, error) {
	return repository.head, nil
}
func (portableRoadmapRepository) Dirty(context.Context) (bool, error) { return false, nil }
func (portableRoadmapRepository) CountCommits(context.Context, string, string) (int, error) {
	return 0, nil
}
func (repository portableRoadmapRepository) Files(_ context.Context, path string) ([]string, error) {
	return append([]string(nil), repository.files[path]...), nil
}
func (portableRoadmapRepository) ContentAtRevision(context.Context, string, string, int64) ([]byte, string, error) {
	return nil, "reference_unavailable", nil
}

func TestRoadmapGraphRoundTripsWithoutClaimingLocalSnapshot(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "roadmap-source")
	lineage := "lineage:abababababababababababababababab"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sourceDBPath(t, sourceDB))
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "roadmap.md"), []byte("portable roadmap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	repository := portableRoadmapRepository{head: operationalCommit, files: map[string][]string{"docs": {"docs/roadmap.md"}}}
	service := roadmap.Service{DB: sourceDB, Root: root, Repo: repository, Now: func() time.Time { return recorded }}
	first, err := service.AppendNode(ctx, roadmap.Node{ProjectID: sourceProject, ID: "portable-roadmap", Title: "Roadmap at " + root + " for " + sourceProject, Status: "proposed", Priority: 20, SourcePath: "docs/roadmap.md", SourceRef: operationalCommit})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AppendNode(ctx, roadmap.Node{ProjectID: sourceProject, ID: first.ID, Title: "Portable roadmap", Status: "active", Priority: 10, Kind: "milestone", TargetRelease: "v1", EvidenceRefs: []string{"commit:" + operationalCommit}, SourcePath: "docs/roadmap.md", SourceRef: operationalCommit})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.AppendNode(ctx, roadmap.Node{ProjectID: sourceProject, ID: "portable-catalog", Title: "Portable catalog", Status: "completed", Priority: 15, SourcePath: "docs/roadmap.md", SourceRef: operationalCommit})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendBinding(ctx, roadmap.Binding{ProjectID: sourceProject, NodeID: first.ID, NodeRevision: first.Revision, Path: "docs/roadmap.md", PathKind: "file", Relationship: "documentation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendBinding(ctx, roadmap.Binding{ProjectID: sourceProject, NodeID: second.ID, NodeRevision: second.Revision, Path: "docs", PathKind: "directory", Relationship: "contract"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNodeLink(ctx, roadmap.NodeLink{ProjectID: sourceProject, NodeID: second.ID, NodeRevision: second.Revision, TargetNodeID: target.ID, Relationship: "depends_on"}); err != nil {
		t.Fatal(err)
	}
	commits, files := 10, 2
	if _, err := service.AppendReviewRoutine(ctx, roadmap.ReviewRoutine{ProjectID: sourceProject, ID: "standard", Title: "Proposed review", Status: "proposed", CommitThreshold: &commits, Source: "docs/roadmap.md", SourceRef: operationalCommit}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendReviewRoutine(ctx, roadmap.ReviewRoutine{ProjectID: sourceProject, ID: "standard", Title: "Accepted review", Status: "accepted", CommitThreshold: &commits, ChangedFilesThreshold: &files, Source: "docs/roadmap.md", SourceRef: operationalCommit}); err != nil {
		t.Fatal(err)
	}
	invocation, err := (attribution.Service{DB: sourceDB}).AppendInvocation(ctx, attribution.Invocation{ID: "roadmap-review-invocation", ProjectID: sourceProject, ActorType: "agent", Client: "codex", Model: "model-a", IdentitySource: "client_hook_claim", OccurredAt: recorded, ReceivedAt: recorded})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordSync(ctx, roadmap.SyncRun{ProjectID: sourceProject, Client: "codex", Model: "model-a", InvocationID: invocation.ID, ReviewedNodes: []string{second.ID, target.ID}, ReviewedFiles: []string{"docs/roadmap.md"}}); err != nil {
		t.Fatal(err)
	}
	if got := tableCount(t, sourceDB, "roadmap_sync_files"); got == 0 {
		t.Fatal("source fixture did not capture a local sync snapshot")
	}

	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	var schemaCounts = map[int]int{}
	for _, record := range loaded.Records {
		if record.RecordType == "roadmap_node" {
			schemaCounts[record.RecordSchemaVersion]++
		}
	}
	if schemaCounts[1] != 2 || schemaCounts[2] != 1 {
		t.Fatalf("roadmap node schema counts = %#v", schemaCounts)
	}
	counts := countSpecs(loaded.Records)
	for recordType, want := range map[string]int{"roadmap_node": 3, "roadmap_binding": 2, "roadmap_link": 1, "roadmap_review_routine": 2, "roadmap_sync": 1, "actor_invocation": 1} {
		if counts[recordType] != want {
			t.Fatalf("%s count=%d want=%d; all=%#v", recordType, counts[recordType], want, counts)
		}
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:roadmap", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded.Add(time.Minute), SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(bundle.RecordsBytes)
	if strings.Contains(serialized, root) || strings.Contains(serialized, sourceProject) || !strings.Contains(serialized, "[PROJECT_ROOT]") || !strings.Contains(serialized, "[PROJECT_ID]") || !strings.Contains(serialized, `"source_snapshot_omitted":true`) {
		t.Fatalf("roadmap export redaction or snapshot marker mismatch: %s", serialized)
	}

	targetCtx, targetDB, targetProject := operationalProject(t, "roadmap-target")
	targetIdentity, err := (Service{DB: targetDB}).Initialize(targetCtx, targetProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	targetRoot := filepath.Dir(sourceDBPath(t, targetDB))
	if err := os.MkdirAll(filepath.Join(targetRoot, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "docs", "roadmap.md"), []byte("portable roadmap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != len(bundle.Records) || plan.WritesPerformed != 0 {
		t.Fatalf("roadmap plan mismatch: %#v err=%v", plan, err)
	}
	applied, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(2*time.Minute))
	if err != nil || applied.Receipt == nil || applied.Receipt.AppendedRecords != len(bundle.Records) {
		t.Fatalf("roadmap apply mismatch: %#v err=%v", applied, err)
	}
	for table, want := range map[string]int{"roadmap_nodes": 3, "roadmap_file_bindings": 2, "roadmap_node_links": 1, "roadmap_review_routines": 2, "roadmap_sync_runs": 1, "roadmap_sync_files": 0} {
		if got := tableCount(t, targetDB, table); got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
	targetService := roadmap.Service{DB: targetDB, Root: targetRoot, Repo: repository, Now: func() time.Time { return recorded.Add(3 * time.Minute) }}
	importedNodes, err := targetService.CurrentNodes(targetCtx, targetProject)
	if err != nil || len(importedNodes) != 2 || importedNodes[0].Kind != "milestone" || importedNodes[0].TargetRelease != "v1" || !reflect.DeepEqual(importedNodes[0].EvidenceRefs, []string{"commit:" + operationalCommit}) {
		t.Fatalf("imported roadmap milestone metadata mismatch: %#v err=%v", importedNodes, err)
	}
	status, err := targetService.Status(targetCtx, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	if status.Freshness != "changed" || len(status.ChangedFiles) == 0 {
		t.Fatalf("imported source sync falsely certified local state: %#v", status)
	}
	reloaded, err := LoadOperationalRecords(targetCtx, targetDB, targetProject)
	if err != nil {
		t.Fatal(err)
	}
	reexport, err := BuildBundle(BundleSpec{ExportID: "export:roadmap-clone", LineageID: lineage, SourceInstanceID: targetIdentity.InstanceID, CreatedAt: recorded.Add(4 * time.Minute), SourceDatabaseSchema: reloaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, reloaded.Records)
	if err != nil {
		t.Fatal(err)
	}
	if string(bundle.RecordsBytes) != string(reexport.RecordsBytes) || bundle.Manifest.RecordSetSHA256 != reexport.Manifest.RecordSetSHA256 {
		t.Fatal("roadmap clone re-export changed semantic records")
	}
	replay, err := ApplyOperationalImport(targetCtx, targetDB, targetProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded.Add(5*time.Minute))
	if err != nil || replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != len(bundle.Records) || replay.Plan.WritesPerformed != 1 {
		t.Fatalf("roadmap replay mismatch: %#v err=%v", replay, err)
	}
}

func TestRoadmapGraphRejectsIsolationCollisionAndMissingNode(t *testing.T) {
	ctx, sourceDB, sourceProject := operationalProject(t, "roadmap-boundaries-source")
	lineage := "lineage:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
	identity, err := (Service{DB: sourceDB}).Initialize(ctx, sourceProject, lineage)
	if err != nil {
		t.Fatal(err)
	}
	recorded := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	service := roadmap.Service{DB: sourceDB, Now: func() time.Time { return recorded }}
	node, err := service.AppendNode(ctx, roadmap.Node{ProjectID: sourceProject, ID: "shared-roadmap", Title: "Source", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendBinding(ctx, roadmap.Binding{ProjectID: sourceProject, NodeID: node.ID, NodeRevision: node.Revision, Path: "docs/roadmap.md", PathKind: "file", Relationship: "documentation"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOperationalRecords(ctx, sourceDB, sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{ExportID: "export:roadmap-boundaries", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: CuratedV1Omissions()}, loaded.Records)
	if err != nil {
		t.Fatal(err)
	}

	isolationCtx, isolationDB, isolationProject := operationalProject(t, "roadmap-isolated")
	if _, err := (Service{DB: isolationDB}).Initialize(isolationCtx, isolationProject, "lineage:efefefefefefefefefefefefefefefef"); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOperationalImport(isolationCtx, isolationDB, isolationProject, bundle.ManifestBytes, bundle.RecordsBytes); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("roadmap bundle crossed isolated lineage: %v", err)
	}

	collisionCtx, collisionDB, collisionProject := operationalProject(t, "roadmap-collision")
	if _, err := (Service{DB: collisionDB}).Initialize(collisionCtx, collisionProject, lineage); err != nil {
		t.Fatal(err)
	}
	if _, err := (roadmap.Service{DB: collisionDB, Now: func() time.Time { return recorded }}).AppendNode(collisionCtx, roadmap.Node{ProjectID: collisionProject, ID: node.ID, Revision: node.Revision, Title: "Different", Status: "blocked", Priority: 99, SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	before := roadmapTableTotal(t, collisionDB)
	plan, err := PlanOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || plan.ApplyAllowed || plan.Counts[PlanCollision] != 1 {
		t.Fatalf("roadmap collision mismatch: %#v err=%v", plan, err)
	}
	if _, err := ApplyOperationalImport(collisionCtx, collisionDB, collisionProject, bundle.ManifestBytes, bundle.RecordsBytes, recorded); err == nil {
		t.Fatal("roadmap collision apply succeeded")
	}
	if after := roadmapTableTotal(t, collisionDB); after != before {
		t.Fatalf("roadmap collision wrote rows: before=%d after=%d", before, after)
	}

	var binding EnvelopeSpec
	for _, spec := range loaded.Records {
		if spec.RecordType == "roadmap_binding" {
			binding = spec
			break
		}
	}
	missing, err := BuildBundle(BundleSpec{ExportID: "export:roadmap-missing", LineageID: lineage, SourceInstanceID: identity.InstanceID, CreatedAt: recorded, SourceDatabaseSchema: loaded.DatabaseSchema, SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile(), Omissions: []Omission{}}, []EnvelopeSpec{binding})
	if err != nil {
		t.Fatal(err)
	}
	emptyCtx, emptyDB, emptyProject := operationalProject(t, "roadmap-empty")
	if _, err := (Service{DB: emptyDB}).Initialize(emptyCtx, emptyProject, lineage); err != nil {
		t.Fatal(err)
	}
	invalid, err := PlanOperationalImport(emptyCtx, emptyDB, emptyProject, missing.ManifestBytes, missing.RecordsBytes)
	if err != nil || invalid.ApplyAllowed || invalid.Counts[PlanInvalid] != 1 || !strings.Contains(invalid.Items[0].Reason, "missing node revision") {
		t.Fatalf("missing roadmap dependency was not rejected: %#v err=%v", invalid, err)
	}
	if got := roadmapTableTotal(t, emptyDB); got != 0 {
		t.Fatalf("invalid roadmap dependency wrote %d rows", got)
	}
}

func roadmapTableTotal(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }) int {
	t.Helper()
	tables := []string{"roadmap_nodes", "roadmap_file_bindings", "roadmap_node_links", "roadmap_review_routines", "roadmap_sync_runs", "roadmap_sync_files", "portable_import_receipts"}
	total := 0
	for _, table := range tables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		total += count
	}
	return total
}
