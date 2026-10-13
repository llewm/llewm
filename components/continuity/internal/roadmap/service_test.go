package roadmap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

type fakeRepository struct {
	head  string
	dirty bool
	count int
	// countErr makes CountCommits fail, as for a watermark this clone lacks.
	countErr error
	files    map[string][]string
	content  map[string][]byte
}

func (repo *fakeRepository) Head(context.Context) (string, error) { return repo.head, nil }
func (repo *fakeRepository) Dirty(context.Context) (bool, error)  { return repo.dirty, nil }
func (repo *fakeRepository) CountCommits(context.Context, string, string) (int, error) {
	return repo.count, repo.countErr
}
func (repo *fakeRepository) Files(_ context.Context, path string) ([]string, error) {
	return repo.files[path], nil
}

func (repo *fakeRepository) ContentAtRevision(_ context.Context, revision, path string, _ int64) ([]byte, string, error) {
	content, ok := repo.content[revision+"\x00"+path]
	if !ok {
		return nil, "reference_unavailable", nil
	}
	return content, "available", nil
}

func testService(t *testing.T) (context.Context, Service, string, *fakeRepository, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{head: "commit-a", files: map[string][]string{}}
	now := time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Root: root, Repo: repo, Now: func() time.Time { return now }}, projectID, repo, root
}

func TestRoadmapStatusTracksCommitsTimeAndAffectedNodes(t *testing.T) {
	ctx, service, projectID, repo, root := testService(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs", "roadmap.md")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "continuity.roadmap", Title: "Roadmap control", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md", SourceRef: "commit-a"})
	if err != nil {
		t.Fatal(err)
	}
	if node.Revision != 1 || node.Authority != "caller_asserted" {
		t.Fatalf("unexpected node: %#v", node)
	}
	if _, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: node.ID, Path: "docs", PathKind: "directory", Relationship: "documentation"}); err != nil {
		t.Fatal(err)
	}
	repo.files["docs"] = []string{"docs/roadmap.md"}
	run, err := service.RecordSync(ctx, SyncRun{ProjectID: projectID, Client: "codex", Model: "model-a", ReviewedNodes: []string{node.ID}, ReviewedFiles: []string{"docs/roadmap.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.GitCommit != "commit-a" || run.Authority != "caller_asserted" || run.GitAuthority != "machine_observed" {
		t.Fatalf("unexpected sync: %#v", run)
	}
	commits, elapsed, files := 3, int64(7200), 1
	routine, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard review", Status: "accepted", CommitThreshold: &commits, ElapsedSecondsThreshold: &elapsed, ChangedFilesThreshold: &files, Source: "docs/roadmap.md"})
	if err != nil {
		t.Fatal(err)
	}
	if routine.Revision != 1 || routine.Authority != "caller_asserted" {
		t.Fatalf("unexpected routine: %#v", routine)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo.head, repo.count, repo.dirty = "commit-b", 3, true
	service.Now = func() time.Time { return run.CompletedAt.Add(2 * time.Hour) }
	status, err := service.Status(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Freshness != "changed" || !status.ReviewDue || len(status.DueReasons) != 3 || status.ReviewRoutine == nil || status.CommitsSinceSync == nil || *status.CommitsSinceSync != 3 || status.ElapsedSeconds == nil || *status.ElapsedSeconds != 7200 || len(status.ChangedFiles) != 1 || len(status.AffectedNodes) != 1 || status.AffectedNodes[0].ID != node.ID {
		t.Fatalf("unexpected roadmap status: %#v", status)
	}
}

func TestLifecycleContextSurfacesOnlyChangedOrDueStatus(t *testing.T) {
	if got := LifecycleContext(Status{Freshness: "current"}); got != "" {
		t.Fatalf("surfaced current roadmap status: %q", got)
	}
	changed := LifecycleContext(Status{
		Freshness:     "changed",
		ChangedFiles:  []FileChange{{Path: "docs/roadmap.md"}},
		AffectedNodes: []AffectedNode{{ID: "continuity.roadmap"}},
	})
	for _, want := range []string{
		"machine-observed",
		"freshness=changed",
		"review_due=false",
		"due_reasons=none",
		"changed_bound_files=1",
		"affected_nodes=1",
		"Roadmap meaning was not reevaluated",
		"no model was invoked",
		"no sync was created",
	} {
		if !strings.Contains(changed, want) {
			t.Fatalf("changed lifecycle context missing %q: %s", want, changed)
		}
	}
	if strings.Contains(changed, "review packet") {
		t.Fatalf("offered a review packet when review was not due: %s", changed)
	}
	due := LifecycleContext(Status{Freshness: "no_sync", ReviewDue: true, DueReasons: []string{"no_model_sync"}})
	for _, want := range []string{"review_due=true", "due_reasons=no_model_sync", "review packet is available on request"} {
		if !strings.Contains(due, want) {
			t.Fatalf("due lifecycle context missing %q: %s", want, due)
		}
	}
}

func TestAcceptedRoutineMakesInitialReviewDueAndRejectsZeroThresholds(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	zero := 0
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "bad", Title: "Bad", Status: "accepted", CommitThreshold: &zero, Source: "docs/roadmap.md"}); err == nil {
		t.Fatal("accepted zero threshold")
	}
	commits := 2
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard", Status: "accepted", CommitThreshold: &commits, Source: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ReviewDue || len(status.DueReasons) != 1 || status.DueReasons[0] != "no_model_sync" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestSupersededRoutineIsNoLongerEffective(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	commits := 2
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard", Status: "accepted", CommitThreshold: &commits, Source: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Retired standard", Status: "superseded", CommitThreshold: &commits, Source: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewRoutine != nil || status.ReviewDue {
		t.Fatalf("superseded routine remained effective: %#v", status)
	}
}

func TestRoadmapStatusWithoutSyncIsExplicitAndBoundedPathsAreValidated(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	status, err := service.Status(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Freshness != "no_sync" || status.LastSync != nil || status.ChangedFiles == nil || status.AffectedNodes == nil {
		t.Fatalf("unexpected empty status: %#v", status)
	}
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "bad", Title: "Bad", Status: "active", SourcePath: "../outside"}); err == nil {
		t.Fatal("accepted escaping source path")
	}
	if _, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: "missing", Path: "docs", PathKind: "directory", Relationship: "documentation"}); err == nil {
		t.Fatal("accepted binding to a missing node")
	}
	if _, err := service.RecordSync(ctx, SyncRun{ProjectID: projectID, ReviewedNodes: []string{"missing"}, ReviewedFiles: []string{"docs/roadmap.md"}}); err == nil {
		t.Fatal("accepted model sync without a model")
	}
}

func TestNodeRevisionsAreAppendOnlyAndBindingsResolveLatest(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	first, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node", Title: "First", Status: "active", SourcePath: "docs/roadmap.md"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node", Title: "Second", Status: "completed", SourcePath: "docs/roadmap.md"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("unexpected revisions: %d, %d", first.Revision, second.Revision)
	}
	binding, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: "node", Path: "docs/roadmap.md", PathKind: "file", Relationship: "contract"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.NodeRevision != 2 {
		t.Fatalf("binding did not resolve latest revision: %#v", binding)
	}
}

func TestReviewPacketReturnsBoundedSourceBackedCurrentNodes(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "roadmap.md"), []byte("roadmap source token=packet-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, node := range []Node{
		{ProjectID: projectID, ID: "later", Title: "Later", Status: "active", Priority: 20, SourcePath: "docs/roadmap.md", SourceRef: "commit-a"},
		{ProjectID: projectID, ID: "first", Title: "First", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md", SourceRef: "commit-a"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	commits := 1
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard", Status: "accepted", CommitThreshold: &commits, Source: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	packet, err := service.ReviewPacket(ctx, projectID, 64, false)
	if err != nil {
		t.Fatal(err)
	}
	if !packet.Status.ReviewDue || len(packet.Nodes) != 2 || packet.Nodes[0].ID != "first" || len(packet.Sources) != 1 || packet.Truncated || packet.Sources[0].Authority != "machine_observed" || packet.Sources[0].SHA256 == "" {
		t.Fatalf("unexpected review packet: %#v", packet)
	}
	if strings.Contains(packet.Sources[0].Content, "packet-secret") {
		t.Fatal("review packet returned unredacted source")
	}
	bounded, err := service.ReviewPacket(ctx, projectID, 12, false)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.ContentBytes != 12 || !bounded.Truncated {
		t.Fatalf("review packet did not enforce byte budget: %#v", bounded)
	}
	if _, err := service.ReviewPacket(ctx, projectID, MaxReviewPacketBytes+1, false); err == nil {
		t.Fatal("accepted oversized packet budget")
	}
}

func TestReviewPacketIsEmptyWhenReviewIsNotDue(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if err := os.WriteFile(filepath.Join(root, "roadmap.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node", Title: "Node", Status: "active", SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	packet, err := service.ReviewPacket(ctx, projectID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Status.ReviewDue || len(packet.Nodes) != 0 || len(packet.Sources) != 0 || packet.ByteBudget != DefaultReviewPacketBytes {
		t.Fatalf("unexpected packet without due review: %#v", packet)
	}
}

func TestManualReviewPacketReturnsSourcesWhenNotDue(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if err := os.WriteFile(filepath.Join(root, "roadmap.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node", Title: "Node", Status: "active", SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	packet, err := service.ReviewPacket(ctx, projectID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if packet.Status.ReviewDue || packet.Trigger != "manual" || len(packet.Nodes) != 1 || len(packet.Sources) != 1 {
		t.Fatalf("unexpected manual packet: %#v", packet)
	}
}

func TestRoadmapOverviewGroupsNodesAndLinks(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	for _, node := range []Node{
		{ProjectID: projectID, ID: "parent", Title: "Parent", Status: "active", Priority: 5, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "focus", Title: "Focus", Status: "active", Priority: 10, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "future", Title: "Future", Status: "deferred", Priority: 30, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "done", Title: "Done", Status: "completed", Priority: 40, SourcePath: "roadmap.md"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	var link NodeLink
	for _, value := range []NodeLink{
		{ProjectID: projectID, NodeID: "parent", TargetNodeID: "focus", Relationship: "parent_of"},
		{ProjectID: projectID, NodeID: "focus", TargetNodeID: "future", Relationship: "depends_on"},
		{ProjectID: projectID, NodeID: "focus", TargetNodeID: "done", Relationship: "depends_on"},
		{ProjectID: projectID, NodeID: "focus", TargetNodeID: "future", Relationship: "parallel_with"},
		{ProjectID: projectID, NodeID: "future", TargetNodeID: "focus", Relationship: "blocks"},
	} {
		var err error
		link, err = service.AppendNodeLink(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
	}
	commits := 2
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard", Status: "accepted", CommitThreshold: &commits, Source: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	overview, err := service.Overview(ctx, projectID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.CurrentFocus) != 2 || len(overview.Future) != 1 || len(overview.Completed) != 1 || len(overview.ChangedNodesSinceSync) != 4 || len(overview.Relationships) != 5 || overview.Relationships[4].ID != link.ID || len(overview.Tracks) != 4 {
		t.Fatalf("unexpected overview: %#v", overview)
	}
	var focus, future Track
	for _, track := range overview.Tracks {
		switch track.Node.ID {
		case "focus":
			focus = track
		case "future":
			future = track
		}
	}
	if len(focus.Parents) != 1 || focus.Parents[0].ID != "parent" || len(focus.DependsOn) != 2 || len(focus.UnresolvedDependencies) != 1 || focus.UnresolvedDependencies[0].ID != "future" || len(focus.ParallelWith) != 1 || len(focus.BlockedBy) != 1 || len(future.ParallelWith) != 1 {
		t.Fatalf("unexpected derived tracks: focus=%#v future=%#v", focus, future)
	}
	if len(overview.ReviewTriggers) != 2 || overview.ReviewTriggers[0].Kind != "model_sync" || overview.ReviewTriggers[0].State != "due" || overview.ReviewTriggers[1].Kind != "commits" || overview.ReviewTriggers[1].State != "not_evaluated" {
		t.Fatalf("unexpected review triggers: %#v", overview.ReviewTriggers)
	}
	if _, err := service.AppendNodeLink(ctx, NodeLink{ProjectID: projectID, NodeID: "focus", TargetNodeID: "focus", Relationship: "depends_on"}); err == nil {
		t.Fatal("accepted self link")
	}
}

func TestRoadmapCoverageReportsCurrentAndPriorRevisionFacts(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	for _, node := range []Node{
		{ProjectID: projectID, ID: "parent", Title: "Parent", Status: "active", Priority: 10, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "child", Title: "Child", Status: "active", Priority: 20, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "orphan", Title: "Orphan", Status: "deferred", Priority: 30, SourcePath: "roadmap.md"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: "parent", NodeRevision: 1, Path: "AGENTS.md", PathKind: "file", Relationship: "contract"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNodeLink(ctx, NodeLink{ProjectID: projectID, NodeID: "parent", NodeRevision: 1, TargetNodeID: "child", Relationship: "parent_of"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "parent", Revision: 2, Title: "Parent revised", Status: "active", Priority: 10, SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	for _, link := range []NodeLink{
		{ProjectID: projectID, NodeID: "parent", NodeRevision: 2, TargetNodeID: "child", Relationship: "parent_of"},
		{ProjectID: projectID, NodeID: "child", NodeRevision: 1, TargetNodeID: "parent", Relationship: "parent_of"},
		{ProjectID: projectID, NodeID: "orphan", NodeRevision: 1, TargetNodeID: "child", Relationship: "parent_of"},
	} {
		if _, err := service.AppendNodeLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.DB.ExecContext(ctx, `INSERT INTO roadmap_node_links(id, project_id, node_id, node_revision, target_node_id, relationship, authority, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "coverage-dangling-link", projectID, "orphan", 1, "missing-target", "parent_of", "legacy_fixture", service.now().Format(time.RFC3339Nano), SchemaVersion); err != nil {
		t.Fatal(err)
	}

	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{Offset: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if coverage.CurrentNodes != 3 || coverage.NodesWithCurrentBindings != 0 || coverage.NodesWithoutCurrentBindings != 3 || coverage.CurrentRevisionBindings != 0 || coverage.PriorRevisionBindings != 1 || coverage.CurrentRelationships != 4 || coverage.PriorRevisionRelationships != 1 || coverage.DanglingRelationshipTargets != 1 || coverage.HierarchyRoots != 1 || coverage.NodesWithMultipleParents != 1 || coverage.HierarchyCycleNodes != 2 {
		t.Fatalf("unexpected coverage totals: %#v", coverage)
	}
	if coverage.Offset != 1 || coverage.Limit != 2 || coverage.Total != 3 || coverage.Returned != 2 || coverage.Omitted != 1 || !coverage.Truncated || !coverage.SourcePathsChecked {
		t.Fatalf("unexpected coverage bounds or scope: %#v", coverage)
	}
	if len(coverage.Nodes) != 2 || coverage.Nodes[0].ID != "child" || coverage.Nodes[1].ID != "orphan" || !coverage.Nodes[0].InHierarchyCycle || !coverage.Nodes[0].MultipleParents || !containsString(coverage.Nodes[0].Observations, "no_current_revision_binding") || len(coverage.Nodes[1].DanglingTargets) != 1 || coverage.Nodes[1].DanglingTargets[0] != "missing-target" {
		t.Fatalf("unexpected coverage page: %#v", coverage.Nodes)
	}
	firstPage, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Nodes) != 1 || firstPage.Nodes[0].ID != "parent" || firstPage.Nodes[0].PriorRevisionBindings != 1 || firstPage.Nodes[0].PriorRevisionRelationships != 1 || !firstPage.Nodes[0].InHierarchyCycle || len(firstPage.Nodes[0].Children) != 1 {
		t.Fatalf("unexpected current/prior revision node coverage: %#v", firstPage.Nodes)
	}
	if _, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: MaxCoverageLimit + 1}); err == nil {
		t.Fatal("accepted a coverage limit above the bound")
	}
}

func TestRoadmapCoverageObservesSourcePathTypesWithoutReturningFileBytes(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "present.md"), []byte("do not return this content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("present.md", filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	for _, node := range []Node{
		{ProjectID: projectID, ID: "a", Title: "Present", Status: "active", SourcePath: "docs/present.md", SourceRef: "HEAD"},
		{ProjectID: projectID, ID: "b", Title: "Missing", Status: "active", SourcePath: "docs/missing.md"},
		{ProjectID: projectID, ID: "c", Title: "Link", Status: "active", SourcePath: "docs/link.md"},
		{ProjectID: projectID, ID: "d", Title: "Directory", Status: "active", SourcePath: "docs"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"regular_file", "missing", "symlink", "other_type"}
	for i, state := range want {
		if coverage.Nodes[i].SourcePathState != state {
			t.Fatalf("node %s path state = %q, want %q", coverage.Nodes[i].ID, coverage.Nodes[i].SourcePathState, state)
		}
	}
	if coverage.Nodes[0].SourceRef != "HEAD" || coverage.Nodes[0].SourceComparison != "unsupported_reference" || strings.Contains(fmt.Sprint(coverage.Nodes), "do not return this content") {
		t.Fatalf("coverage did not preserve reference/scope: %#v", coverage)
	}
}

func TestRoadmapCoverageComparesBoundedSourceToExactRevision(t *testing.T) {
	ctx, service, projectID, repository, root := testService(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"docs/match.md": "same", "docs/different.md": "current"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ref := strings.Repeat("a", 40)
	repository.content = map[string][]byte{
		ref + "\x00docs/match.md":     []byte("same"),
		ref + "\x00docs/different.md": []byte("historical"),
	}
	for _, node := range []Node{
		{ProjectID: projectID, ID: "a-match", Title: "Match", Status: "active", SourcePath: "docs/match.md", SourceRef: ref},
		{ProjectID: projectID, ID: "b-different", Title: "Different", Status: "active", SourcePath: "docs/different.md", SourceRef: ref},
		{ProjectID: projectID, ID: "c-unsupported", Title: "Unsupported", Status: "active", SourcePath: "docs/match.md", SourceRef: "HEAD"},
		{ProjectID: projectID, ID: "d-no-reference", Title: "No reference", Status: "active", SourcePath: "docs/match.md"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"matches_reference", "differs_from_reference", "unsupported_reference", "no_reference"}
	for i, state := range want {
		if coverage.Nodes[i].SourceComparison != state {
			t.Fatalf("node %s comparison = %q, want %q", coverage.Nodes[i].ID, coverage.Nodes[i].SourceComparison, state)
		}
	}
}

func TestRoadmapCoverageComparesExpectedScopeWithNarrativeTouches(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "scope-node", Title: "Scope", Status: "active", SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []Binding{
		{ProjectID: projectID, NodeID: "scope-node", Path: "internal/covered.go", PathKind: "file", Relationship: "implementation"},
		{ProjectID: projectID, NodeID: "scope-node", Path: "docs", PathKind: "directory", Relationship: "documentation"},
		{ProjectID: projectID, NodeID: "scope-node", Path: "internal/missing.go", PathKind: "file", Relationship: "implementation"},
	} {
		if _, err := service.AppendBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	narratives := worknarrative.Service{DB: service.DB, Root: root}
	if _, err := narratives.Append(ctx, worknarrative.Record{
		ProjectID: projectID, Title: "Observed work", Trigger: "manual", Creator: "test",
		Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Changed files.", Authority: "caller_asserted"}},
		Anchors: []worknarrative.Anchor{
			{Kind: "roadmap_node", Target: "scope-node", Authority: "caller_asserted"},
			{Kind: "file", Target: "internal/covered.go", ContentSHA256: strings.Repeat("a", 64), Authority: "machine_observed"},
			{Kind: "file", Target: "docs/guide.md", ContentSHA256: strings.Repeat("b", 64), Authority: "machine_observed"},
			{Kind: "file", Target: "outside.go", ContentSHA256: strings.Repeat("c", 64), Authority: "machine_observed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	node := coverage.Nodes[0]
	if len(node.ExpectedScope) != 3 || !reflect.DeepEqual(node.ObservedTouches, []string{"docs/guide.md", "internal/covered.go", "outside.go"}) || !reflect.DeepEqual(node.ExpectedWithoutObserved, []string{"internal/missing.go"}) || !reflect.DeepEqual(node.ObservedOutsideExpected, []string{"outside.go"}) || node.ScopeEvidenceTruncated {
		t.Fatalf("unexpected scope evidence: %#v", node)
	}
	for _, observation := range []string{"expected_scope_without_observed_touch", "observed_touch_outside_expected_scope"} {
		if !containsString(node.Observations, observation) {
			t.Fatalf("missing %s in %#v", observation, node.Observations)
		}
	}
}

func TestRoadmapCoverageRollsUpMilestoneChildrenBlockersAndEvidence(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/roadmap.md"), []byte("roadmap"), 0o600); err != nil {
		t.Fatal(err)
	}
	nodes := []Node{
		{ProjectID: projectID, ID: "milestone", Title: "Milestone", Kind: "milestone", Status: "active", SourcePath: "docs/roadmap.md", TargetRelease: "v1", EvidenceRefs: []string{"decision:milestone"}},
		{ProjectID: projectID, ID: "done", Title: "Done", Kind: "work_item", Status: "completed", SourcePath: "docs/roadmap.md", EvidenceRefs: []string{"commit:done"}},
		{ProjectID: projectID, ID: "blocked", Title: "Blocked", Kind: "research", Status: "blocked", SourcePath: "docs/roadmap.md"},
		{ProjectID: projectID, ID: "active", Title: "Active", Kind: "work_item", Status: "active", SourcePath: "docs/roadmap.md"},
	}
	for _, node := range nodes {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range []string{"done", "blocked", "active"} {
		if _, err := service.AppendNodeLink(ctx, NodeLink{ProjectID: projectID, NodeID: "milestone", TargetNodeID: child, Relationship: "parent_of"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AppendNodeLink(ctx, NodeLink{ProjectID: projectID, NodeID: "active", TargetNodeID: "blocked", Relationship: "depends_on"}); err != nil {
		t.Fatal(err)
	}
	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	var milestone *CoverageNode
	for i := range coverage.Nodes {
		if coverage.Nodes[i].ID == "milestone" {
			milestone = &coverage.Nodes[i]
		}
	}
	if milestone == nil || milestone.Kind != "milestone" || milestone.TargetRelease != "v1" || milestone.Milestone == nil {
		t.Fatalf("missing milestone rollup: %#v", milestone)
	}
	want := &MilestoneRollup{ChildCount: 3, CompletedChildren: 1, ActiveChildren: 1, BlockedChildren: 1, BlockerNodeIDs: []string{"blocked"}, EvidenceRefs: []string{"commit:done", "decision:milestone"}}
	if !reflect.DeepEqual(milestone.Milestone, want) {
		t.Fatalf("milestone rollup = %#v, want %#v", milestone.Milestone, want)
	}
}

func TestRoadmapNodeKindAndMilestoneTargetsAreValidated(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	base := Node{ProjectID: projectID, ID: "node", Title: "Node", Status: "active", SourcePath: "docs/roadmap.md"}
	invalid := base
	invalid.Kind = "epic"
	if _, err := service.AppendNode(ctx, invalid); err == nil || !strings.Contains(err.Error(), "unsupported roadmap node kind") {
		t.Fatalf("unexpected kind error: %v", err)
	}
	invalid = base
	invalid.Kind, invalid.TargetDate = "work_item", "2026-12-01"
	if _, err := service.AppendNode(ctx, invalid); err == nil || !strings.Contains(err.Error(), "only for milestone") {
		t.Fatalf("unexpected target error: %v", err)
	}
}

func TestRoadmapNodeHistoryReportsBoundedFieldChanges(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	base := Node{ProjectID: projectID, ID: "history-node", Title: "Original", Status: "proposed", Priority: 10, SourcePath: "docs/old.md", SourceRef: strings.Repeat("a", 40)}
	if _, err := service.AppendNode(ctx, base); err != nil {
		t.Fatal(err)
	}
	second := base
	second.Revision, second.Status = 2, "active"
	if _, err := service.AppendNode(ctx, second); err != nil {
		t.Fatal(err)
	}
	third := second
	third.Revision, third.Status, third.Priority, third.SourcePath, third.SourceRef = 3, "completed", 20, "docs/new.md", strings.Repeat("b", 40)
	if _, err := service.AppendNode(ctx, third); err != nil {
		t.Fatal(err)
	}

	firstPage, err := service.NodeHistory(ctx, projectID, NodeHistoryOptions{NodeID: "history-node", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if firstPage.Total != 3 || firstPage.Returned != 2 || firstPage.Omitted != 1 || !firstPage.Truncated || firstPage.Revisions[0].Current || !reflect.DeepEqual(firstPage.Revisions[0].ChangedFields, []string{"initial_revision"}) || !reflect.DeepEqual(firstPage.Revisions[1].ChangedFields, []string{"status"}) {
		t.Fatalf("unexpected first history page: %#v", firstPage)
	}
	lastPage, err := service.NodeHistory(ctx, projectID, NodeHistoryOptions{NodeID: "history-node", Offset: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"priority", "source_path", "source_ref", "status"}
	if lastPage.Returned != 1 || !lastPage.Revisions[0].Current || !reflect.DeepEqual(lastPage.Revisions[0].ChangedFields, wantFields) {
		t.Fatalf("unexpected final history revision: %#v", lastPage)
	}
	if _, err := service.NodeHistory(ctx, projectID, NodeHistoryOptions{NodeID: "history-node", Limit: MaxNodeHistoryLimit + 1}); err == nil {
		t.Fatal("accepted a node history page over the configured bound")
	}
}

func TestRoadmapTreePaginatesRootsAndMarksUnrootedCycles(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	for _, node := range []Node{
		{ProjectID: projectID, ID: "root", Title: "Root", Status: "active", Priority: 10, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "child", Title: "Child", Status: "active", Priority: 20, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "orphan", Title: "Orphan", Status: "deferred", Priority: 30, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "cycle-a", Title: "Cycle A", Status: "active", Priority: 40, SourcePath: "roadmap.md"},
		{ProjectID: projectID, ID: "cycle-b", Title: "Cycle B", Status: "active", Priority: 50, SourcePath: "roadmap.md"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []NodeLink{
		{ProjectID: projectID, NodeID: "root", TargetNodeID: "child", Relationship: "parent_of"},
		{ProjectID: projectID, NodeID: "cycle-a", TargetNodeID: "cycle-b", Relationship: "parent_of"},
		{ProjectID: projectID, NodeID: "cycle-b", TargetNodeID: "cycle-a", Relationship: "parent_of"},
	} {
		if _, err := service.AppendNodeLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.DB.ExecContext(ctx, `INSERT INTO roadmap_node_links(id,project_id,node_id,node_revision,target_node_id,relationship,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?)`, "tree-dangling", projectID, "orphan", 1, "missing-target", "parent_of", "legacy_fixture", service.now().Format(time.RFC3339Nano), SchemaVersion); err != nil {
		t.Fatal(err)
	}
	firstPage, err := service.Tree(ctx, projectID, TreeOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if firstPage.Nodes != 5 || firstPage.Total != 2 || firstPage.Returned != 1 || firstPage.Omitted != 1 || !firstPage.Truncated || len(firstPage.Entries) < 2 || firstPage.Entries[0].ID != "root" || firstPage.Entries[0].Depth != 0 || firstPage.Entries[1].ID != "child" || firstPage.Entries[1].ParentID != "root" || firstPage.Entries[1].Depth != 1 || len(firstPage.UnrootedNodes) != 2 || firstPage.HierarchyCycleNodes != 2 || len(firstPage.DanglingTargets) != 1 {
		t.Fatalf("unexpected first tree page: %#v", firstPage)
	}
	lastPage, err := service.Tree(ctx, projectID, TreeOptions{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(lastPage.Entries) == 0 || lastPage.Entries[0].ID != "orphan" || lastPage.Entries[0].Revision != 1 || lastPage.Total != 2 || !lastPage.Truncated {
		t.Fatalf("unexpected final tree page: %#v", lastPage)
	}
	if _, err := service.Tree(ctx, projectID, TreeOptions{Limit: MaxTreeLimit + 1}); err == nil {
		t.Fatal("accepted a tree page over the configured bound")
	}
}

func TestRoadmapProjectionIsCanonicalByteStableAndCheckIsReadOnly(t *testing.T) {
	ctx, service, projectID, _, root := testService(t)
	service.ProjectName = "Projection test"
	for _, node := range []Node{
		{ProjectID: projectID, ID: "z-deferred", Title: "Deferred", Status: "deferred", Priority: 30, SourcePath: "docs/roadmap.md"},
		{ProjectID: projectID, ID: "b-active", Title: "Active", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md"},
		{ProjectID: projectID, ID: "a-active", Title: "First", Status: "accepted", Priority: 10, SourcePath: "docs/roadmap.md"},
		{ProjectID: projectID, ID: "done", Title: "Done", Status: "completed", Priority: 40, SourcePath: "docs/roadmap.md"},
	} {
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range []NodeLink{
		{ProjectID: projectID, NodeID: "b-active", TargetNodeID: "z-deferred", Relationship: "depends_on"},
		{ProjectID: projectID, NodeID: "a-active", TargetNodeID: "b-active", Relationship: "parent_of"},
	} {
		if _, err := service.AppendNodeLink(ctx, link); err != nil {
			t.Fatal(err)
		}
	}
	commits := 5
	if _, err := service.AppendReviewRoutine(ctx, ReviewRoutine{ProjectID: projectID, ID: "standard", Title: "Standard", Status: "accepted", CommitThreshold: &commits, Source: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}

	first, err := service.Render(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Render(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("projection was not byte-stable:\nfirst=%#v\nsecond=%#v", first, second)
	}
	for _, want := range []string{"DO NOT EDIT", "Graph hash:", "Renderer:", "Content hash:", "## Active tracks", "## Completed work", "## Deferred work", "Unresolved dependencies: `z-deferred` (deferred)", "## Review triggers"} {
		if !strings.Contains(first.Markdown, want) {
			t.Fatalf("projection missing %q:\n%s", want, first.Markdown)
		}
	}
	if strings.Index(first.Markdown, "`a-active`") > strings.Index(first.Markdown, "`b-active`") {
		t.Fatalf("equal-priority nodes were not ordered by ID:\n%s", first.Markdown)
	}

	missing, err := service.CheckProjection(ctx, projectID, "docs/generated-roadmap.md")
	if err != nil || missing.State != "missing" || missing.ActualHash != "" {
		t.Fatalf("unexpected missing check: result=%#v err=%v", missing, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs", "generated-roadmap.md")
	if err := os.WriteFile(path, []byte(first.Markdown), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := service.CheckProjection(ctx, projectID, "docs/generated-roadmap.md")
	if err != nil || current.State != "current" || current.ActualHash != current.ExpectedHash {
		t.Fatalf("unexpected current check: result=%#v err=%v", current, err)
	}
	if err := os.WriteFile(path, []byte(first.Markdown+"manual edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drifted, err := service.CheckProjection(ctx, projectID, "docs/generated-roadmap.md")
	if err != nil || drifted.State != "drifted" || drifted.ActualHash == drifted.ExpectedHash {
		t.Fatalf("unexpected drifted check: result=%#v err=%v", drifted, err)
	}
	afterEdit, err := service.Render(ctx, projectID)
	if err != nil || afterEdit.GraphHash != first.GraphHash || afterEdit.Markdown != first.Markdown {
		t.Fatalf("manual projection edit affected graph input: projection=%#v err=%v", afterEdit, err)
	}
	if _, err := service.CheckProjection(ctx, projectID, "../outside.md"); err == nil {
		t.Fatal("accepted projection path outside project")
	}
}

func TestRoadmapStatusReportsAWatermarkThisCloneLacks(t *testing.T) {
	ctx, service, projectID, repo, _ := testService(t)
	node, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "continuity.roadmap", Title: "Roadmap control", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md", SourceRef: "commit-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: node.ID, Path: "docs", PathKind: "directory", Relationship: "documentation"}); err != nil {
		t.Fatal(err)
	}
	repo.files["docs"] = []string{"docs/roadmap.md"}
	if _, err := service.RecordSync(ctx, SyncRun{ProjectID: projectID, Client: "codex", Model: "model-a", ReviewedNodes: []string{node.ID}, ReviewedFiles: []string{"docs/roadmap.md"}}); err != nil {
		t.Fatal(err)
	}
	// The review was synced from a machine whose code this clone does not have.
	repo.head, repo.countErr = "commit-old", errors.New("git rev-list: bad revision")
	status, err := service.Status(ctx, projectID)
	if err != nil || status.Freshness != "watermark_unavailable" || status.CommitsSinceSync != nil {
		t.Fatalf("status: %#v %v", status, err)
	}
	line := LifecycleContext(status)
	if !strings.Contains(line, "freshness=watermark_unavailable") || !strings.Contains(line, "commit-a") || !strings.Contains(line, "pull first") {
		t.Fatalf("lifecycle context = %q", line)
	}
}
