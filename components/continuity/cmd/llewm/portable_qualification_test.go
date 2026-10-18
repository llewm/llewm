package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

type portableQualificationReport struct {
	A1Export struct {
		ManifestBytes       int    `json:"manifest_bytes"`
		RecordsBytes        int    `json:"records_bytes"`
		RecordCount         int    `json:"record_count"`
		EstimatedTokens     int    `json:"estimated_tokens"`
		EstimateMethod      string `json:"estimate_method"`
		RecordsStable       bool   `json:"records_stable"`
		RecordSetHashStable bool   `json:"record_set_hash_stable"`
		RecordTypes         int    `json:"record_types"`
		OmissionFamilies    int    `json:"omission_families"`
		MissingReferences   int    `json:"missing_references"`
	} `json:"a1_export"`
	A2Recovery struct {
		PlannedAppend   int  `json:"planned_append"`
		AppliedRecords  int  `json:"applied_records"`
		InitialWrites   int  `json:"initial_writes"`
		ReplayAlready   int  `json:"replay_already_present"`
		ReplayWrites    int  `json:"replay_writes"`
		ReexportRecords int  `json:"reexport_records_bytes"`
		ReexportStable  bool `json:"reexport_stable"`
	} `json:"a2_recovery"`
	Isolation struct {
		LineageBRejected     bool `json:"lineage_b_rejected"`
		BRowsBefore          int  `json:"b_rows_before"`
		BRowsAfter           int  `json:"b_rows_after"`
		CollisionCount       int  `json:"collision_count"`
		CollisionApplyDenied bool `json:"collision_apply_denied"`
		CollisionRowsBefore  int  `json:"collision_rows_before"`
		CollisionRowsAfter   int  `json:"collision_rows_after"`
	} `json:"isolation"`
	TaskOutcome string `json:"task_outcome"`
}

func TestPortableMemoryTwoProjectCLIQualification(t *testing.T) {
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	sandbox := t.TempDir()
	t.Chdir(sandbox)
	a1 := qualificationProject(t, sandbox, "a1")
	a2 := qualificationProject(t, sandbox, "a2")
	b := qualificationProject(t, sandbox, "b")
	collision := qualificationProject(t, sandbox, "a-collision")
	lineageA := "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lineageB := "lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, fixture := range []struct{ root, lineage string }{{a1, lineageA}, {a2, lineageA}, {b, lineageB}, {collision, lineageA}} {
		runCLI(t, "", "--project-root", fixture.root, "init")
		runCLI(t, "", "--project-root", fixture.root, "sync", "init", "--lineage", fixture.lineage)
	}

	seedCompletePortableProfile(t, a1)
	runCLI(t, "", "--project-root", b, "narrative", "record", "--id", "portable-work", "--title", "Isolated B", "--trigger", "manual", "--claims-json", `[{"kind":"outcome","text":"Lineage B reuses the local record ID safely.","authority":"caller_asserted"}]`)
	runCLI(t, "", "--project-root", collision, "narrative", "record", "--id", "portable-work", "--title", "Conflicting A", "--trigger", "manual", "--claims-json", `[{"kind":"outcome","text":"Same lineage and identity, different envelope.","authority":"caller_asserted"}]`)

	a1ExportDir := filepath.Join(sandbox, "export-a1")
	a1SecondDir := filepath.Join(sandbox, "export-a1-second")
	bExportDir := filepath.Join(sandbox, "export-b")
	var a1Export, a1Second, bExport portable.ExportResult
	decodeCLI(t, runCLI(t, "", "--project-root", a1, "memory", "export", "--directory", a1ExportDir), &a1Export)
	decodeCLI(t, runCLI(t, "", "--project-root", a1, "memory", "export", "--directory", a1SecondDir), &a1Second)
	decodeCLI(t, runCLI(t, "", "--project-root", b, "memory", "export", "--directory", bExportDir), &bExport)
	aManifest, aRecords, err := portable.ReadBundleDirectory(a1ExportDir)
	if err != nil {
		t.Fatal(err)
	}
	_, aSecondRecords, err := portable.ReadBundleDirectory(a1SecondDir)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(aManifest, []byte("portable-secret")) || bytes.Contains(aRecords, []byte("portable-secret")) || bytes.Contains(aRecords, []byte(a1)) || !bytes.Contains(aRecords, []byte("[PROJECT_ROOT]")) || !bytes.Contains(aRecords, []byte("[REDACTED]")) {
		t.Fatalf("portable export leaked seeded sensitive or local data: manifest=%s records=%s", aManifest, aRecords)
	}
	if !bytes.Equal(aRecords, aSecondRecords) || a1Export.Manifest.RecordSetSHA256 != a1Second.Manifest.RecordSetSHA256 {
		t.Fatal("unchanged A1 exports changed semantic bytes or record-set hash")
	}
	var inspection portable.BundleInspection
	decodeCLI(t, runCLI(t, "", "memory", "inspect", "--directory", a1ExportDir), &inspection)
	if inspection.ManifestBytes != a1Export.ManifestBytes || inspection.RecordsBytes != a1Export.RecordsBytes || inspection.RecordCount != a1Export.RecordCount || inspection.BodiesLoaded != 0 {
		t.Fatalf("inspection measurements do not match export: export=%#v inspect=%#v", a1Export, inspection)
	}
	wantRecordCounts := map[string]int{
		"actor_invocation": 1, "checkpoint": 3, "checkpoint_action": 3, "checkpoint_action_transition": 4, "checkpoint_context": 3,
		"work_item": 2, "work_item_disposition": 1, "work_item_link": 1, "roadmap_binding": 1, "roadmap_link": 1, "roadmap_node": 2,
		"roadmap_review_routine": 1, "roadmap_sync": 1,
		"work_narrative": 2, "work_narrative_supersession": 1,
		"track_plan": 1,
	}
	if !reflect.DeepEqual(inspection.RecordCounts, wantRecordCounts) {
		t.Fatalf("complete-profile record counts mismatch: got=%#v want=%#v", inspection.RecordCounts, wantRecordCounts)
	}
	wantOmissions := map[string]string{"contract_index_entries": "local_index_of_repository_markdown", "raw_events": "profile_excluded", "raw_sessions": "profile_excluded", "usage_samples": "profile_excluded"}
	gotOmissions := make([]string, 0, len(inspection.Manifest.Omissions))
	for _, omission := range inspection.Manifest.Omissions {
		if wantOmissions[omission.RecordFamily] != omission.Reason {
			t.Fatalf("unexpected omission reason: %#v", omission)
		}
		gotOmissions = append(gotOmissions, omission.RecordFamily)
	}
	sort.Strings(gotOmissions)
	wantOmissionNames := make([]string, 0, len(wantOmissions))
	for name := range wantOmissions {
		wantOmissionNames = append(wantOmissionNames, name)
	}
	sort.Strings(wantOmissionNames)
	if !reflect.DeepEqual(gotOmissions, wantOmissionNames) || len(a1Export.MissingReferences) != 0 {
		t.Fatalf("curated-v1 omissions or references mismatch: omissions=%#v missing=%#v", gotOmissions, a1Export.MissingReferences)
	}

	var plan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "--project-root", a2, "memory", "plan", "--directory", a1ExportDir), &plan)
	if !plan.ApplyAllowed || plan.Counts[portable.PlanAppend] != a1Export.RecordCount || plan.WritesPerformed != 0 {
		t.Fatalf("unexpected A2 plan: %#v", plan)
	}
	var applied, replay portable.ImportApplyResult
	decodeCLI(t, runCLI(t, "", "--project-root", a2, "memory", "apply", "--directory", a1ExportDir, "--confirm-lineage", lineageA, "--confirm-project", plan.ProjectID), &applied)
	decodeCLI(t, runCLI(t, "", "--project-root", a2, "memory", "apply", "--directory", a1ExportDir, "--confirm-lineage", lineageA, "--confirm-project", plan.ProjectID), &replay)
	if applied.Receipt == nil || applied.Receipt.AppendedRecords != a1Export.RecordCount || applied.Plan.WritesPerformed != a1Export.RecordCount+1 {
		t.Fatalf("unexpected first apply: %#v", applied)
	}
	if replay.Receipt == nil || replay.Receipt.AlreadyPresentRecords != a1Export.RecordCount || replay.Receipt.AppendedRecords != 0 || replay.Plan.WritesPerformed != 1 {
		t.Fatalf("unexpected replay: %#v", replay)
	}
	a2ExportDir := filepath.Join(sandbox, "export-a2")
	var a2Export portable.ExportResult
	decodeCLI(t, runCLI(t, "", "--project-root", a2, "memory", "export", "--directory", a2ExportDir), &a2Export)
	_, a2Records, err := portable.ReadBundleDirectory(a2ExportDir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aRecords, a2Records) || a1Export.Manifest.RecordSetSHA256 != a2Export.Manifest.RecordSetSHA256 {
		t.Fatal("same-lineage recovery changed semantic export")
	}

	bBefore := qualificationPortableRows(t, b)
	if err, _ := runCLIError("--project-root", b, "memory", "plan", "--directory", a1ExportDir); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("lineage B plan was not rejected before writes: %v", err)
	}
	bAfter := qualificationPortableRows(t, b)
	if bBefore != bAfter {
		t.Fatalf("lineage rejection changed B rows: before=%d after=%d", bBefore, bAfter)
	}
	var collisionPlan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "--project-root", collision, "memory", "plan", "--directory", a1ExportDir), &collisionPlan)
	if collisionPlan.ApplyAllowed || collisionPlan.Counts[portable.PlanCollision] != 1 || collisionPlan.WritesPerformed != 0 || qualificationReceipts(t, collision) != 0 {
		t.Fatalf("same-lineage collision was not a zero-write refusal: %#v", collisionPlan)
	}
	collisionRowsBefore := qualificationPortableRows(t, collision)
	collisionErr, _ := runCLIError("--project-root", collision, "memory", "apply", "--directory", a1ExportDir, "--confirm-lineage", lineageA, "--confirm-project", collisionPlan.ProjectID)
	collisionRowsAfter := qualificationPortableRows(t, collision)
	if collisionErr == nil || collisionRowsBefore != collisionRowsAfter || qualificationReceipts(t, collision) != 0 {
		t.Fatalf("collision apply did not fail atomically: err=%v before=%d after=%d", collisionErr, collisionRowsBefore, collisionRowsAfter)
	}

	if err := os.WriteFile(filepath.Join(a1ExportDir, "records.jsonl"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tamperRowsBefore := qualificationPortableRows(t, a2)
	var tamperPlan portable.ImportPlan
	decodeCLI(t, runCLI(t, "", "--project-root", a2, "memory", "plan", "--directory", a1ExportDir), &tamperPlan)
	tamperRowsAfter := qualificationPortableRows(t, a2)
	if tamperPlan.ApplyAllowed || tamperPlan.Counts[portable.PlanInvalid] == 0 || tamperPlan.WritesPerformed != 0 || tamperRowsBefore != tamperRowsAfter {
		t.Fatalf("tampered import plan was accepted or mutated state: plan=%#v before=%d after=%d", tamperPlan, tamperRowsBefore, tamperRowsAfter)
	}

	var report portableQualificationReport
	report.A1Export.ManifestBytes = a1Export.ManifestBytes
	report.A1Export.RecordsBytes = a1Export.RecordsBytes
	report.A1Export.RecordCount = a1Export.RecordCount
	report.A1Export.EstimatedTokens = (a1Export.ManifestBytes + a1Export.RecordsBytes + 3) / 4
	report.A1Export.EstimateMethod = "low_confidence_utf8_bytes_div_4"
	report.A1Export.RecordsStable = bytes.Equal(aRecords, aSecondRecords)
	report.A1Export.RecordSetHashStable = a1Export.Manifest.RecordSetSHA256 == a1Second.Manifest.RecordSetSHA256
	report.A1Export.RecordTypes = len(inspection.RecordCounts)
	report.A1Export.OmissionFamilies = len(gotOmissions)
	report.A1Export.MissingReferences = len(a1Export.MissingReferences)
	report.A2Recovery.PlannedAppend = plan.Counts[portable.PlanAppend]
	report.A2Recovery.AppliedRecords = applied.Receipt.AppendedRecords
	report.A2Recovery.InitialWrites = applied.Plan.WritesPerformed
	report.A2Recovery.ReplayAlready = replay.Receipt.AlreadyPresentRecords
	report.A2Recovery.ReplayWrites = replay.Plan.WritesPerformed
	report.A2Recovery.ReexportRecords = a2Export.RecordsBytes
	report.A2Recovery.ReexportStable = bytes.Equal(aRecords, a2Records)
	report.Isolation.LineageBRejected = true
	report.Isolation.BRowsBefore = bBefore
	report.Isolation.BRowsAfter = bAfter
	report.Isolation.CollisionCount = collisionPlan.Counts[portable.PlanCollision]
	report.Isolation.CollisionApplyDenied = collisionErr != nil
	report.Isolation.CollisionRowsBefore = collisionRowsBefore
	report.Isolation.CollisionRowsAfter = collisionRowsAfter
	report.TaskOutcome = "passed"
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("portable CLI qualification measurements: %s", encoded)
}

func seedCompletePortableProfile(t *testing.T, root string) {
	t.Helper()
	application, err := app.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	ctx := t.Context()
	recorded := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	invocation, err := application.Attribution.AppendInvocation(ctx, attribution.Invocation{
		ID: "portable-invocation", ProjectID: application.ProjectID, SessionID: "portable-session",
		ActorType: "agent", Client: "codex", Provider: "openai", Model: "model-a",
		IdentitySource: "client_hook_claim", OccurredAt: recorded, ReceivedAt: recorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.Checkpoints.CreateWithActions(ctx, checkpoint.Checkpoint{
		ID: "portable-checkpoint", ProjectID: application.ProjectID, TaskID: "portable-memory",
		Task: "Qualify portable memory", Status: "in_progress", Summary: "Complete profile at " + root,
		NextAction: "Recover every curated family", Completed: []string{"Built complete fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var actionID string
	if err := application.DB.QueryRowContext(ctx, `SELECT id FROM checkpoint_actions WHERE source_checkpoint_id=?`, created.Checkpoint.ID).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Checkpoints.TransitionAction(ctx, application.ProjectID, "", actionID, "completed", "Qualified"); err != nil {
		t.Fatal(err)
	}

	application.Roadmap.Now = func() time.Time { return recorded.Add(2 * time.Minute) }
	node, err := application.Roadmap.AppendNode(ctx, roadmap.Node{ProjectID: application.ProjectID, ID: "portable-memory", Title: "Portable memory", Status: "active", Priority: 10, SourcePath: "AGENTS.md", SourceRef: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := application.Roadmap.AppendNode(ctx, roadmap.Node{ProjectID: application.ProjectID, ID: "portable-catalog", Title: "Portable catalog", Status: "completed", Priority: 20, SourcePath: "AGENTS.md", SourceRef: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Roadmap.AppendBinding(ctx, roadmap.Binding{ProjectID: application.ProjectID, NodeID: node.ID, NodeRevision: node.Revision, Path: "AGENTS.md", PathKind: "file", Relationship: "contract"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Roadmap.AppendNodeLink(ctx, roadmap.NodeLink{ProjectID: application.ProjectID, NodeID: node.ID, NodeRevision: node.Revision, TargetNodeID: target.ID, Relationship: "depends_on"}); err != nil {
		t.Fatal(err)
	}
	commits := 5
	if _, err := application.Roadmap.AppendReviewRoutine(ctx, roadmap.ReviewRoutine{ProjectID: application.ProjectID, ID: "portable-review", Title: "Portable review", Status: "accepted", CommitThreshold: &commits, Source: "AGENTS.md", SourceRef: "HEAD"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Roadmap.RecordSync(ctx, roadmap.SyncRun{ProjectID: application.ProjectID, Client: "codex", Model: "model-a", InvocationID: invocation.ID, ReviewedNodes: []string{node.ID, target.ID}, ReviewedFiles: []string{"AGENTS.md"}}); err != nil {
		t.Fatal(err)
	}

	application.WorkItems.Now = func() time.Time { return recorded.Add(4 * time.Minute) }
	ideaRecord, err := application.WorkItems.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "portable-idea", ProjectID: application.ProjectID, Kind: "idea", Title: "Portable idea", Body: "Review " + application.ProjectID + " token=idea-secret", TaskID: "portable-memory", CheckpointID: created.Checkpoint.ID, Creator: "codex", Model: "model-a", Tags: []string{"portable"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.WorkItems.Dispose(ctx, workitem.Disposition{ProjectID: application.ProjectID, ItemID: ideaRecord.ID, Status: "integrated", Reason: "Qualified token=disposition-secret", Creator: "human-reviewer", TargetRefs: []string{"roadmap:" + node.ID}}); err != nil {
		t.Fatal(err)
	}
	application.WorkItems.Now = func() time.Time { return recorded.Add(5 * time.Minute) }
	bug, err := application.WorkItems.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "portable-bug", ProjectID: application.ProjectID, Kind: "bug", Title: "Repeated friction", Body: "Keep the portable profile complete at " + root, Observed: "cache failures", Severity: "medium", RoadmapNodeID: node.ID, Creator: "human-reviewer"}, From: "friction:1234567890abcdef1234567890abcdef"})
	if err != nil {
		t.Fatal(err)
	}

	application.Narratives.Now = func() time.Time { return recorded.Add(6 * time.Minute) }
	narrative, err := application.Narratives.Append(ctx, worknarrative.Record{ID: "portable-work", ProjectID: application.ProjectID, Title: "Portable work", Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Qualified at " + root + " token=portable-secret.", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "task", Target: "portable-memory", Authority: "caller_asserted"}}, Creator: "codex", CreatorInvocationID: invocation.ID})
	if err != nil {
		t.Fatal(err)
	}
	application.Narratives.Now = func() time.Time { return recorded.Add(7 * time.Minute) }
	if _, err := application.Narratives.Append(ctx, worknarrative.Record{ID: "portable-work-corrected", ProjectID: application.ProjectID, Title: "Portable work corrected", Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Qualified every family.", Authority: "caller_asserted"}}, Anchors: []worknarrative.Anchor{{Kind: "task", Target: "portable-memory", Authority: "caller_asserted"}}, Creator: "codex", CreatorInvocationID: invocation.ID, SupersedesID: narrative.ID}); err != nil {
		t.Fatal(err)
	}

	application.TrackPlans.Now = func() time.Time { return recorded.Add(8 * time.Minute) }
	if _, err := application.TrackPlans.AppendPlan(ctx, trackplan.Plan{ProjectID: application.ProjectID, ID: "portable-plan", RoadmapNodeID: node.ID, TaskID: "portable-memory", Title: "Portable plan", Summary: "Exercise complete portability", Status: "accepted", Creator: "human-reviewer", WorkItemIDs: []string{bug.ID}, Items: []trackplan.Item{{ID: "round-trip", Position: 1, Title: "Round trip", Detail: "Export and recover", Status: "ready", EvidenceRefs: []string{"narrative:portable-work"}, WorkItemIDs: []string{bug.ID}}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := application.Checkpoints.TransitionTask(ctx, checkpoint.TaskTransition{ProjectID: application.ProjectID, Operation: "start", TaskID: "portable-source", RequestKey: "portable-start-source"}); err != nil {
		t.Fatal(err)
	}
	sourceCheckpoint, err := application.Checkpoints.Create(ctx, checkpoint.Checkpoint{ID: "portable-source-checkpoint", ProjectID: application.ProjectID, TaskID: "portable-source", Task: "Portable source", Status: "paused", Summary: "Pause for tangent", NextAction: "Return after tangent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Checkpoints.TransitionTask(ctx, checkpoint.TaskTransition{ProjectID: application.ProjectID, Operation: "fork", TaskID: "portable-source", TargetTaskID: "portable-tangent", CheckpointID: sourceCheckpoint.ID, RequestKey: "portable-fork-tangent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Checkpoints.Create(ctx, checkpoint.Checkpoint{ID: "portable-tangent-checkpoint", ProjectID: application.ProjectID, TaskID: "portable-tangent", Task: "Portable tangent", Status: "in_progress", Summary: "Qualify transition", NextAction: "Return"}); err != nil {
		t.Fatal(err)
	}
}

func qualificationProject(t *testing.T, parent, name string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.llewm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "AGENTS.md", ".gitignore"}, {"-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "Initialize fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func decodeCLI(t *testing.T, body string, destination any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), destination); err != nil {
		t.Fatalf("decode CLI JSON: %v\n%s", err, body)
	}
}

func runCLIError(args ...string) (error, string) {
	command := newRootCommand("llewm")
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	return command.Execute(), output.String()
}

func qualificationPortableRows(t *testing.T, root string) int {
	t.Helper()
	application, err := app.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	tables := []string{"actor_invocations", "checkpoints", "checkpoint_actions", "checkpoint_action_transitions", "work_narratives", "work_item_dispositions", "work_item_links", "work_items", "roadmap_file_bindings", "roadmap_node_links", "roadmap_nodes", "roadmap_review_routines", "roadmap_sync_runs", "track_plan_revisions", "portable_import_receipts"}
	rows := 0
	for _, table := range tables {
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE project_id=?", table)
		if table == "checkpoint_action_transitions" {
			query = "SELECT COUNT(*) FROM checkpoint_action_transitions WHERE action_id IN (SELECT id FROM checkpoint_actions WHERE project_id=?)"
		}
		var count int
		if err := application.DB.QueryRowContext(t.Context(), query, application.ProjectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		rows += count
	}
	return rows
}

func qualificationReceipts(t *testing.T, root string) int {
	t.Helper()
	application, err := app.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	var rows int
	if err := application.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM portable_import_receipts WHERE project_id=?`, application.ProjectID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
