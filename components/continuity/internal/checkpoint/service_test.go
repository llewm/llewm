package checkpoint

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func testService(t *testing.T) Service {
	t.Helper()
	db, err := database.Initialize(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p','/test','test','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	return Service{DB: db}
}

// continueTip names the task's current tip as predecessor, as a caller that has
// read the tip does. New tasks keep an empty predecessor.
func continueTip(t *testing.T, s Service, value Checkpoint) Checkpoint {
	t.Helper()
	if value.PredecessorCheckpointID == "" && value.TaskID != "" {
		tip, err := currentTaskTip(context.Background(), s.DB, value.ProjectID, value.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		value.PredecessorCheckpointID = tip
	}
	return value
}

// insertLegacy writes a checkpoint row directly, as pre-lineage history did,
// bypassing the task and predecessor rules that now refuse such rows.
func insertLegacy(t *testing.T, s Service, value Checkpoint) Checkpoint {
	t.Helper()
	value.ID, value.CreatedAt = activity.NewID(), time.Now().UTC()
	if value.Status == "" {
		value.Status = "in_progress"
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO checkpoints(id,project_id,task_id,task_title,status,summary,next_action,payload_json,created_at) VALUES (?,?,NULLIF(?,''),?,?,?,?,?,?)`,
		value.ID, value.ProjectID, value.TaskID, value.Task, value.Status, value.Summary, value.NextAction, string(payload), value.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestValidationDoesNotWrite(t *testing.T) {
	s := testService(t)
	for _, change := range []func(*Checkpoint){
		func(c *Checkpoint) { c.Task = " \n" },
		func(c *Checkpoint) { c.NextAction = "\t" },
		func(c *Checkpoint) { c.ProjectID = "" },
		func(c *Checkpoint) { c.Status = "invented" },
		func(c *Checkpoint) { c.Summary = strings.Repeat("x", MaxBytes) },
		func(c *Checkpoint) { c.Verified = make([]string, 65) },
		func(c *Checkpoint) { c.Completed = []string{" "} },
		func(c *Checkpoint) { c.Attempts = []Attempt{{Description: "Run tests"}} },
		func(c *Checkpoint) { c.Attempts = make([]Attempt, 65) },
		func(c *Checkpoint) { c.Metadata = map[string]any{"invalid": make(chan int)} },
	} {
		c := Checkpoint{ProjectID: "p", Task: "Task", NextAction: "Next"}
		change(&c)
		if _, err := s.Create(context.Background(), c); err == nil {
			t.Fatalf("accepted invalid checkpoint")
		}
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted %d invalid checkpoints", count)
	}
}

func TestRedactionAndAppendOnly(t *testing.T) {
	s := testService(t)
	secret := "fixture-sensitive-value"
	c := Checkpoint{ProjectID: "p", TaskID: "redaction", Task: "token=" + secret, NextAction: "password='" + secret + "'", Summary: `{"api_key":"` + secret + `"}`, Authority: "machine_observed",
		Completed: []string{"secret=" + secret}, Changes: []string{"token=" + secret}, Verified: []string{"Authorization: Bearer " + secret}, EvidenceRefs: []string{"https://test.invalid?token=" + secret},
		Attempts: []Attempt{{Description: "token=" + secret, Outcome: "failure", Reason: "password=" + secret}},
		Metadata: map[string]any{"nested": map[string]any{"api_key": secret, "stdin": secret, "environment": map[string]any{"unusual": secret}}}}
	created, err := s.Create(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if created.Authority != "caller_asserted" {
		t.Fatal("caller promoted authority")
	}
	if c.Completed[0] != "secret="+secret {
		t.Fatal("mutated input")
	}
	var task, summary, next, payload string
	if err := s.DB.QueryRow(`SELECT task_title,summary,next_action,payload_json FROM checkpoints WHERE id=?`, created.ID).Scan(&task, &summary, &next, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task+summary+next+payload, secret) {
		t.Fatal("secret reached database")
	}
	if !strings.Contains(payload, "[REDACTED]") {
		t.Fatal("missing redaction markers")
	}
	latest, err := s.Latest(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(latest)
	if strings.Contains(string(body), secret) || len(latest.Attempts) != 1 || len(latest.EvidenceRefs) != 1 {
		t.Fatal("incorrect readback")
	}
	if _, err := s.Create(context.Background(), Checkpoint{ProjectID: "p", TaskID: "new-task", Task: "New task", NextAction: "Next"}); err != nil {
		t.Fatal(err)
	}
	var preserved string
	if err := s.DB.QueryRow(`SELECT payload_json FROM checkpoints WHERE id=?`, created.ID).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != payload {
		t.Fatal("rewrote earlier checkpoint")
	}
	if _, err := s.Create(context.Background(), Checkpoint{ID: created.ID, ProjectID: "p", Task: "Replacement", NextAction: "Next"}); err == nil {
		t.Fatal("replaced existing checkpoint")
	}
}

func TestLegacyCheckpointRead(t *testing.T) {
	s := testService(t)
	old := `{"id":"old","project_id":"p","task":"Legacy task","status":"in_progress","summary":"","next_action":"Next","verified":["go test"],"created_at":"2026-01-01T00:00:00Z"}`
	if _, err := s.DB.Exec(`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES ('old','p','Legacy task','in_progress','','Next',?,'2026-01-01T00:00:00Z')`, old); err != nil {
		t.Fatal(err)
	}
	got, err := s.Latest(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "Legacy task" || len(got.Verified) != 1 {
		t.Fatal("legacy handoff lost")
	}
}

func TestUnresolvedActionsCarryForwardAndResolveAppendOnly(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	first, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "work", Task: "First", NextAction: "Run first check"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.CarriedForward) != 0 {
		t.Fatalf("unexpected predecessors: %#v", first.CarriedForward)
	}
	second, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "work", PredecessorCheckpointID: first.Checkpoint.ID, Task: "Second", NextAction: "Run second check"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.CarriedForward) != 1 || second.CarriedForward[0].Description != "Run first check" || second.CarriedForward[0].Status != "carried_forward" {
		t.Fatalf("missing carry-forward: %#v", second)
	}
	actions, err := s.Unresolved(ctx, "p", "work", second.Checkpoint.ID)
	if err != nil || len(actions) != 1 || actions[0].ID != second.CarriedForward[0].ID {
		t.Fatalf("unexpected predecessor actions: %#v %v", actions, err)
	}
	if _, err := s.TransitionAction(ctx, "p", "work", actions[0].ID, "completed", "verified manually"); err != nil {
		t.Fatal(err)
	}
	actions, err = s.Unresolved(ctx, "p", "work", "")
	if err != nil || len(actions) != 1 || actions[0].Description != "Run second check" {
		t.Fatalf("resolved action remained open: %#v %v", actions, err)
	}
	var transitions int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoint_action_transitions WHERE action_id=?`, second.CarriedForward[0].ID).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 3 {
		t.Fatalf("expected append-only open, carried, completed transitions; got %d", transitions)
	}
	if _, err := s.TransitionAction(ctx, "p", "", second.CarriedForward[0].ID, "superseded", "changed"); err == nil {
		t.Fatal("re-transitioned resolved action")
	}
}

func TestCompletedCheckpointAtomicallyReconcilesSameTaskWithoutCreatingAction(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	_, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "Work", NextAction: "Finish the work"})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.Unresolved(ctx, "p", "task-a", "")
	if err != nil || len(actions) != 1 {
		t.Fatalf("unexpected initial actions: %#v %v", actions, err)
	}
	actionID := actions[0].ID
	secret := "atomic-resolution-secret"
	if _, err := s.CreateWithReconciliation(ctx, continueTip(t, s, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "Work", Status: "completed", NextAction: "Task complete; await the next objective."}), ActionReconciliation{}); err == nil || !strings.Contains(err.Error(), "must reconcile or explicitly carry forward") {
		t.Fatalf("completed checkpoint did not reject unresolved same-task action: %v", err)
	}
	var checkpoints int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&checkpoints); err != nil || checkpoints != 1 {
		t.Fatalf("rejected checkpoint persisted: count=%d err=%v", checkpoints, err)
	}
	result, err := s.CreateWithReconciliation(ctx, continueTip(t, s, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "Work", Status: "completed", NextAction: "Task complete; await the next objective."}), ActionReconciliation{Updates: []ActionUpdate{{ID: actions[0].ID, Status: "completed", Reason: "token=" + secret}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ActionCreated || len(result.Reconciled) != 1 || result.Reconciled[0].Status != "completed" || len(result.CarriedForward) != 0 {
		t.Fatalf("unexpected terminal create result: %#v", result)
	}
	actions, err = s.Unresolved(ctx, "p", "task-a", "")
	if err != nil || len(actions) != 0 {
		t.Fatalf("terminal checkpoint left actions unresolved: %#v %v", actions, err)
	}
	var linkedCheckpoint, reason string
	if err := s.DB.QueryRow(`SELECT checkpoint_id,reason FROM checkpoint_action_transitions WHERE action_id=? AND status='completed'`, actionID).Scan(&linkedCheckpoint, &reason); err != nil {
		t.Fatal(err)
	}
	if linkedCheckpoint != result.Checkpoint.ID || strings.Contains(reason, secret) || !strings.Contains(reason, "[REDACTED]") {
		t.Fatalf("resolution not linked to terminal checkpoint: checkpoint=%q reason=%q", linkedCheckpoint, reason)
	}
}

func actionIDForCheckpoint(t *testing.T, s Service, checkpointID string) string {
	t.Helper()
	var id string
	if err := s.DB.QueryRow(`SELECT id FROM checkpoint_actions WHERE source_checkpoint_id=?`, checkpointID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCompletedCheckpointRequiresExplicitSameTaskCarryAndLeavesOtherTasksAlone(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	first, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", NextAction: "Continue A"})
	if err != nil {
		t.Fatal(err)
	}
	actionID := actionIDForCheckpoint(t, s, first.Checkpoint.ID)
	carried, err := s.CreateWithReconciliation(ctx, continueTip(t, s, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", Status: "completed", NextAction: "A is complete, but this action remains delegated."}), ActionReconciliation{CarryForwardActionIDs: []string{actionID}})
	if err != nil {
		t.Fatal(err)
	}
	if carried.ActionCreated || len(carried.CarriedForward) != 1 || carried.CarriedForward[0].ID != actionID {
		t.Fatalf("explicit carry-forward was not preserved: %#v", carried)
	}
	unrelated, err := s.CreateWithReconciliation(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", Task: "B", Status: "completed", NextAction: "Task B complete; await the next objective."}, ActionReconciliation{})
	if err != nil {
		t.Fatal(err)
	}
	if unrelated.ActionCreated || len(unrelated.CarriedForward) != 0 {
		t.Fatalf("another task's action was absorbed: %#v", unrelated)
	}
	remaining, err := s.Unresolved(ctx, "p", "task-a", "")
	if err != nil || len(remaining) != 1 || remaining[0].ID != actionID || remaining[0].CarriedToCheckpoint != carried.Checkpoint.ID || remaining[0].TaskID != "task-a" {
		t.Fatalf("task-a's delegated action did not stay with task-a: %#v, %v", remaining, err)
	}
}

func TestParallelTasksNeverTouchEachOthersActions(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	a, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", SessionID: "agent-a", Task: "A", NextAction: "Continue A"})
	if err != nil {
		t.Fatal(err)
	}
	actionA := actionIDForCheckpoint(t, s, a.Checkpoint.ID)
	b, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", SessionID: "agent-b", Task: "B", NextAction: "Continue B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.CarriedForward) != 0 {
		t.Fatalf("task-b absorbed task-a's action: %#v", b.CarriedForward)
	}
	actionB := actionIDForCheckpoint(t, s, b.Checkpoint.ID)
	for name, candidate := range map[string]Checkpoint{
		"new task":            {ProjectID: "p", TaskID: "task-c", Task: "C", NextAction: "Start C"},
		"strict continuation": {ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: a.Checkpoint.ID, Task: "A", NextAction: "Still A"},
	} {
		for _, reconciliation := range []ActionReconciliation{
			{Updates: []ActionUpdate{{ID: actionB, Status: "completed"}}},
			{CarryForwardActionIDs: []string{actionB}},
		} {
			if _, err := s.CreateWithReconciliation(ctx, candidate, reconciliation); err == nil || !strings.Contains(err.Error(), `belongs to task "task-b"`) {
				t.Fatalf("%s: cross-task reconciliation was not rejected: %v", name, err)
			}
		}
	}
	if _, err := s.TransitionAction(ctx, "p", "task-a", actionB, "completed", "not mine"); err == nil {
		t.Fatal("task-a closed task-b's action")
	}
	for task, want := range map[string]string{"task-a": actionA, "task-b": actionB} {
		actions, err := s.Unresolved(ctx, "p", task, "")
		if err != nil || len(actions) != 1 || actions[0].ID != want || actions[0].Status != "open" {
			t.Fatalf("%s actions = %#v, %v", task, actions, err)
		}
	}
	if _, err := s.ListActions(ctx, "p", "", false); err == nil || !strings.Contains(err.Error(), "task-a: 1, task-b: 1") {
		t.Fatalf("unscoped listing across tasks was not refused: %v", err)
	}
	if all, err := s.ListActions(ctx, "p", "", true); err != nil || len(all) != 2 {
		t.Fatalf("all actions = %#v, %v", all, err)
	}
}

func TestStrictContinuationRequiresCurrentSameTaskPredecessor(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	first, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", NextAction: "Continue A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: first.ID, Task: "A", NextAction: "Continue A again"})
	if err != nil {
		t.Fatal(err)
	}
	if second.PredecessorCheckpointID != first.ID {
		t.Fatalf("lineage not preserved: %#v", second)
	}
	for name, candidate := range map[string]Checkpoint{
		"stale":        {ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: first.ID, Task: "A", NextAction: "Branch"},
		"wrong task":   {ProjectID: "p", TaskID: "task-b", PredecessorCheckpointID: second.ID, Task: "B", NextAction: "Continue B"},
		"missing task": {ProjectID: "p", PredecessorCheckpointID: second.ID, Task: "A", NextAction: "Continue"},
		"unknown":      {ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: "missing", Task: "A", NextAction: "Continue"},
	} {
		if _, err := s.Create(ctx, candidate); err == nil {
			t.Fatalf("%s strict continuation unexpectedly succeeded", name)
		}
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejected continuation wrote a checkpoint: count=%d err=%v", count, err)
	}
}

func TestCrossTaskActionDispositionIsRejected(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	a, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", NextAction: "Continue A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateWithActions(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", Task: "B", NextAction: "Continue B"})
	if err != nil {
		t.Fatal(err)
	}
	actionB := actionIDForCheckpoint(t, s, b.Checkpoint.ID)
	candidate := Checkpoint{ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: a.Checkpoint.ID, Task: "A", NextAction: "Still A"}
	_, err = s.CreateWithReconciliation(ctx, candidate, ActionReconciliation{Updates: []ActionUpdate{{ID: actionB, Status: "completed"}}})
	if err == nil || !strings.Contains(err.Error(), `belongs to task "task-b", not "task-a"`) {
		t.Fatalf("cross-task action disposition was not rejected: %v", err)
	}
}

func TestCheckpointRequestKeyReplaysWithoutWrites(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	candidate := Checkpoint{ProjectID: "p", TaskID: "task-a", RequestKey: "save-1", Task: "A", Summary: "state", NextAction: "Continue A"}
	first, err := s.CreateWithActions(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWithActions(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if second.Checkpoint.ID != first.Checkpoint.ID || second.ActionCreated != first.ActionCreated {
		t.Fatalf("retry did not replay original result: first=%#v second=%#v", first, second)
	}
	var checkpoints, actions int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkpoint_actions`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 || actions != 1 {
		t.Fatalf("retry wrote duplicates: checkpoints=%d actions=%d", checkpoints, actions)
	}
	candidate.Summary = "different"
	if _, err := s.CreateWithActions(ctx, candidate); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("request-key collision was not rejected: %v", err)
	}
}

func TestExplicitTaskLifecycleOperationsEnforceManagedLineage(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	legacy, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", NextAction: "Continue A"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "task-a", CheckpointID: legacy.ID, RequestKey: "start-a", Reason: "adopt strict lineage"})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "task-a", CheckpointID: legacy.ID, RequestKey: "start-a", Reason: "adopt strict lineage"})
	if err != nil || replayed.ID != started.ID {
		t.Fatalf("task transition retry did not replay: %#v %v", replayed, err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "task-a", CheckpointID: legacy.ID, RequestKey: "start-a", Reason: "different"}); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("task request-key collision was not rejected: %v", err)
	}
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "A", NextAction: "Skip the tip"}); err == nil || !strings.Contains(err.Error(), "already has checkpoints") {
		t.Fatalf("managed task accepted legacy append: %v", err)
	}
	a2, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", PredecessorCheckpointID: legacy.ID, Task: "A", NextAction: "Continue again"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "fork", TaskID: "task-a", TargetTaskID: "task-b", CheckpointID: legacy.ID, RequestKey: "fork-stale"}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("fork accepted stale tip: %v", err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "fork", TaskID: "task-a", TargetTaskID: "task-b", CheckpointID: a2.ID, RequestKey: "fork-b"}); err != nil {
		t.Fatal(err)
	}
	b1, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", Task: "B", NextAction: "Continue B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "rekey", TaskID: "task-b", TargetTaskID: "task-c", CheckpointID: b1.ID, RequestKey: "rekey-c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", PredecessorCheckpointID: b1.ID, Task: "B", NextAction: "Continue terminal task"}); err == nil || !strings.Contains(err.Error(), "rekeyed") {
		t.Fatalf("rekeyed task accepted checkpoint: %v", err)
	}
	c1, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-c", Task: "C", NextAction: "Continue C"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "task-d", RequestKey: "start-d"}); err != nil {
		t.Fatal(err)
	}
	d1, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-d", Task: "D", NextAction: "Continue D"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "supersede", TaskID: "task-d", TargetTaskID: "task-c", CheckpointID: d1.ID, RequestKey: "supersede-d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "task-e", RequestKey: "start-e"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "abandon", TaskID: "task-e", RequestKey: "abandon-e"}); err != nil {
		t.Fatal(err)
	}
	if c1.ID == "" {
		t.Fatal("missing rekey target checkpoint")
	}
	lineage, err := s.TaskLineage(ctx, "p", "task-c", 10)
	if err != nil || !lineage.State.Managed || lineage.State.OriginOperation != "rekey" || lineage.CurrentTip != c1.ID || len(lineage.Events) != 2 || lineage.Events[0].Operation != "rekey" || lineage.Events[1].Operation != "supersede" {
		t.Fatalf("unexpected task lineage read: %#v %v", lineage, err)
	}
}

func TestLineageAnomalyReportIsDeterministicAndReadOnly(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	// Two roots of one task are legacy history; current saves refuse them.
	insertLegacy(t, s, Checkpoint{ProjectID: "p", TaskID: "split", Task: "Split", NextAction: "One"})
	insertLegacy(t, s, Checkpoint{ProjectID: "p", TaskID: "split", Task: "Split", NextAction: "Two"})
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: "done", RequestKey: "start-done"}); err != nil {
		t.Fatal(err)
	}
	done, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "done", Task: "Done", Status: "completed", NextAction: "Await work"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.LineageAnomalies(ctx, "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.LineageAnomalies(ctx, "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(first)
	two, _ := json.Marshal(second)
	if string(one) != string(two) || len(first.Anomalies) != 1 {
		t.Fatalf("report not stable or complete: %s / %s", one, two)
	}
	if first.Anomalies[0].TaskID != "done" || !containsString(first.Anomalies[0].Codes, "terminal_selection") || first.Anomalies[0].SelectedCheckpointID != done.ID {
		t.Fatalf("missing terminal selection anomaly: %#v", first.Anomalies[0])
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestLatestSelectedIsolatesTaskSessionAndProject(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	first, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", SessionID: "session-a", Task: "First", NextAction: "Continue first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-b", SessionID: "session-b", Task: "Second", NextAction: "Continue second"})
	if err != nil {
		t.Fatal(err)
	}
	for name, selection := range map[string]Selection{
		"checkpoint": {CheckpointID: first.ID},
		"task":       {TaskID: "task-a"},
		"session":    {SessionID: "session-a"},
	} {
		got, err := s.LatestSelected(ctx, "p", selection)
		if err != nil || got == nil || got.ID != first.ID {
			t.Fatalf("%s selection returned %#v, %v", name, got, err)
		}
	}
	latest, err := s.LatestSelected(ctx, "p", Selection{})
	if err != nil || latest == nil || latest.ID != second.ID {
		t.Fatalf("legacy latest returned %#v, %v", latest, err)
	}
	missing, err := s.LatestSelected(ctx, "p", Selection{TaskID: "stale-task"})
	if err != nil || missing != nil {
		t.Fatalf("stale selection returned %#v, %v", missing, err)
	}
	crossProject, err := s.LatestSelected(ctx, "other-project", Selection{CheckpointID: first.ID})
	if err != nil || crossProject != nil {
		t.Fatalf("cross-project selection returned %#v, %v", crossProject, err)
	}
	if _, err := s.LatestSelected(ctx, "p", Selection{TaskID: "task-a", SessionID: "session-a"}); err == nil {
		t.Fatal("accepted ambiguous selection")
	}
}

func TestFreshnessReportsBoundedObservedActivity(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	created, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task-a", Task: "Task", NextAction: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	store := activity.Store{DB: s.DB}
	for _, name := range []string{"posttooluse", "postcompact"} {
		event := activity.NewEvent("p", "codex", name, "continuity")
		if err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.Freshness(ctx, "p", Selection{TaskID: "task-a"}, 1, ReminderPolicy{MaxEvents: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Checkpoint == nil || report.Checkpoint.ID != created.ID || report.Freshness != "activity_since_checkpoint" || report.EventsSince != 2 || len(report.RecentActivity) != 1 || !report.ActivityTruncated || report.ActivityScope != "project" || report.RepositoryAuthority != "machine_observed" || report.CheckpointAuthority != "caller_asserted" || !report.ReminderEvaluated || !report.CheckpointRecommended || len(report.ReminderReasons) != 1 || report.ReminderReasons[0] != "event_threshold" {
		t.Fatalf("unexpected freshness report: %#v", report)
	}
	if _, err := s.Freshness(ctx, "p", Selection{}, MaxActivityLimit+1, ReminderPolicy{}); err == nil {
		t.Fatal("accepted oversized activity limit")
	}
	if _, err := s.Freshness(ctx, "p", Selection{}, 0, ReminderPolicy{MaxAgeSeconds: -1}); err == nil {
		t.Fatal("accepted negative reminder age")
	}
	if _, err := s.Freshness(ctx, "p", Selection{}, 0, ReminderPolicy{MaxEvents: MaxReminderEvents + 1}); err == nil {
		t.Fatal("accepted oversized reminder event threshold")
	}
	missing, err := s.Freshness(ctx, "p", Selection{TaskID: "missing"}, 0, ReminderPolicy{MaxAgeSeconds: 60})
	if err != nil || missing.SchemaVersion != 2 || missing.Checkpoint != nil || missing.Freshness != "no_checkpoint" || missing.EventsSince != 0 || missing.EventsObserved != 2 || len(missing.RecentActivity) != 2 || missing.ActivityTruncated || !missing.CheckpointRecommended || len(missing.ReminderReasons) != 1 || missing.ReminderReasons[0] != "no_checkpoint" || !strings.Contains(missing.Disclaimer, "cannot establish task intent") {
		t.Fatalf("unexpected missing checkpoint report: %#v, %v", missing, err)
	}
}

func TestReminderEdgeIsAtomicAndRearmsAfterCheckpoint(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "task", SessionID: "session-a", Task: "Task", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	store := activity.Store{DB: s.DB}
	tool := func(client, session string) {
		t.Helper()
		event := activity.NewEvent("p", client, "posttooluse", "tool")
		event.SessionID = session
		if err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	tool("codex", "session-a")

	const workers = 8
	start := make(chan struct{})
	results := make(chan ReminderEdge, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := s.EvaluateReminderEdge(ctx, "p", "codex", "session-a", "task", ReminderPolicy{MaxEvents: 1})
			results <- result
			errors <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	surfaced := 0
	for result := range results {
		if result.ShouldSurface {
			surfaced++
		}
	}
	if surfaced != 1 {
		t.Fatalf("expected one atomic reminder edge, got %d", surfaced)
	}

	if _, err := s.Create(ctx, continueTip(t, s, Checkpoint{ProjectID: "p", TaskID: "task", SessionID: "session-a", Task: "Task", NextAction: "Continue again"})); err != nil {
		t.Fatal(err)
	}
	tool("codex", "session-a")
	rearmed, err := s.EvaluateReminderEdge(ctx, "p", "codex", "session-a", "task", ReminderPolicy{MaxEvents: 1})
	if err != nil || !rearmed.ShouldSurface {
		t.Fatalf("new checkpoint did not rearm reminder: %#v, %v", rearmed, err)
	}
}

func TestRemindersFollowTheSessionNotOtherAgents(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	store := activity.Store{DB: s.DB}
	tool := func(session string) {
		t.Helper()
		event := activity.NewEvent("p", "codex", "posttooluse", "tool")
		event.SessionID = session
		if err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	policy := ReminderPolicy{MaxEvents: 3}
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "mine", SessionID: "me", Task: "Mine", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	tool("me")
	tool("me")
	// Another agent's busy activity does not count toward my threshold...
	for range 5 {
		tool("other")
	}
	if edge, err := s.EvaluateReminderEdge(ctx, "p", "codex", "me", "mine", policy); err != nil || edge.ShouldSurface || edge.Freshness.EventsSince != 2 {
		t.Fatalf("other session's events counted: %#v %v", edge.Freshness, err)
	}
	// ...and its checkpoint does not reset my clock.
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "theirs", SessionID: "other", Task: "Theirs", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	tool("me")
	edge, err := s.EvaluateReminderEdge(ctx, "p", "codex", "me", "mine", policy)
	if err != nil || !edge.ShouldSurface || edge.Freshness.Checkpoint == nil || edge.Freshness.Checkpoint.TaskID != "mine" {
		t.Fatalf("my reminder followed another task: %#v %v", edge.Freshness, err)
	}
	// The other session gets its own reminder for the same project.
	for range 3 {
		tool("other")
	}
	if theirs, err := s.EvaluateReminderEdge(ctx, "p", "codex", "other", "theirs", policy); err != nil || !theirs.ShouldSurface {
		t.Fatalf("one session's reminder suppressed another's: %#v %v", theirs.Freshness, err)
	}
	// A session without a checkpoint counts from its own first event.
	tool("fresh")
	tool("fresh")
	tool("fresh")
	if fresh, err := s.EvaluateReminderEdge(ctx, "p", "codex", "fresh", "", ReminderPolicy{MaxEvents: 2}); err != nil || !fresh.ShouldSurface || !containsString(fresh.Freshness.ReminderReasons, "no_checkpoint") {
		t.Fatalf("checkpoint-less session reminder: %#v %v", fresh.Freshness, err)
	}
}

func TestFindPagesLiteralMatchesAndKeepsLatest(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		payload := `{"id":"` + id + `","project_id":"p","task":"Needle 100%_` + id + `","status":"in_progress","summary":"historical match","next_action":"Continue","authority":"caller_asserted","created_at":"2026-01-02T00:00:00Z"}`
		if _, err := s.DB.Exec(`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES (?, 'p', ?, 'in_progress', 'historical match', 'Continue', ?, '2026-01-02T00:00:00Z')`, id, "Needle 100%_"+id, payload); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Find(ctx, "p", FindRequest{Query: "100%_", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Hits) != 2 || first.Hits[0].ID != "c" || first.Hits[1].ID != "b" || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := s.Find(ctx, "p", FindRequest{Query: "100%_", Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Hits) != 1 || second.Hits[0].ID != "a" || second.NextCursor != "" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	missing, err := s.Find(ctx, "p", FindRequest{Query: "not present"})
	if err != nil || len(missing.Hits) != 0 || missing.Hits == nil {
		t.Fatalf("unexpected no-match result: %#v %v", missing, err)
	}
	latest, err := s.Latest(ctx, "p")
	if err != nil || latest == nil || latest.ID != "c" {
		t.Fatalf("history read changed latest: %#v %v", latest, err)
	}
}

func TestGetIsProjectScopedBoundedAndReadsLegacy(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	legacy := `{"id":"old","project_id":"p","task":"Legacy handoff","status":"completed","summary":"` + strings.Repeat("detail ", 200) + `","next_action":"Do not silently resume","verified":["caller claim"],"created_at":"2026-01-01T00:00:00Z"}`
	if _, err := s.DB.Exec(`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES ('old','p','Legacy handoff','completed','summary','Do not silently resume',?,'2026-01-01T00:00:00Z')`, legacy); err != nil {
		t.Fatal(err)
	}
	export, err := s.Get(ctx, "p", "old", 512)
	if err != nil {
		t.Fatal(err)
	}
	if export == nil || export.ID != "old" || !strings.Contains(export.Markdown, "Legacy handoff") || !strings.Contains(export.Markdown, "Export truncated") || !export.Truncated || len(export.Markdown) > 512 || export.Authority != "caller_asserted" {
		t.Fatalf("unexpected export: %#v", export)
	}
	if other, err := s.Get(ctx, "other-project", "old", 512); err != nil || other != nil {
		t.Fatalf("cross-project lookup leaked: %#v %v", other, err)
	}
	for _, request := range []FindRequest{{Query: ""}, {Query: strings.Repeat("x", 257)}, {Query: "x", Limit: 101}, {Query: "x", Cursor: "bad"}} {
		if _, err := s.Find(ctx, "p", request); err == nil {
			t.Fatalf("accepted invalid find request: %#v", request)
		}
	}
}

func TestEverySaveNamesItsTaskAndCurrentTip(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", Task: "Loose", NextAction: "Continue"}); err == nil || !strings.Contains(err.Error(), "requires a task_id") {
		t.Fatalf("untasked checkpoint was accepted: %v", err)
	}
	// An unmanaged task with history: a blind append would silently become
	// another agent's tip, so it is refused with the tip to continue from.
	first, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "shared", SessionID: "agent-a", Task: "Shared", NextAction: "A continues"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "shared", SessionID: "agent-b", Task: "Shared", NextAction: "B appends blindly"}); err == nil || !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("blind append to an existing task was accepted: %v", err)
	}
	second, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "shared", PredecessorCheckpointID: first.ID, SessionID: "agent-b", Task: "Shared", NextAction: "B takes over at the tip"})
	if err != nil {
		t.Fatal(err)
	}
	// Agent A still holds the old tip; its save is stale, not silently interleaved.
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "shared", PredecessorCheckpointID: first.ID, SessionID: "agent-a", Task: "Shared", NextAction: "A again"}); err == nil || !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), second.ID) {
		t.Fatalf("stale continuation was accepted: %v", err)
	}
}

func TestRetiredTaskActionsRemainHistoricalWithoutStartupReminders(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	old, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "old", Task: "Old rollout", NextAction: "Restart"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "review", Task: "Review rollout", Status: "completed", NextAction: "Complete"})
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range []Checkpoint{old, review} {
		if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "start", TaskID: cp.TaskID, CheckpointID: cp.ID, RequestKey: "start-" + cp.TaskID}); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := s.OtherTaskActionCount(ctx, "p", "review"); err != nil || count != 1 {
		t.Fatalf("before retirement: %d %v", count, err)
	}
	if _, err := s.TransitionTask(ctx, TaskTransition{ProjectID: "p", Operation: "supersede", TaskID: "old", TargetTaskID: "review", CheckpointID: old.ID, RequestKey: "retire-old"}); err != nil {
		t.Fatal(err)
	}
	if count, err := s.OtherTaskActionCount(ctx, "p", "review"); err != nil || count != 0 {
		t.Fatalf("retired action still advertised: %d %v", count, err)
	}
	actions, err := s.ListActions(ctx, "p", "old", false)
	if err != nil || len(actions) != 1 || actions[0].Status != "open" {
		t.Fatalf("historical action overwritten: %+v %v", actions, err)
	}
}
