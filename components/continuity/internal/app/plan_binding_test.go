package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func bindingApp(t *testing.T) *App {
	t.Helper()
	a := fenceApp(t)
	a.Roadmap = roadmap.Service{DB: a.DB}
	for _, id := range []string{"node", "other"} {
		if _, err := a.Roadmap.AppendNode(context.Background(), roadmap.Node{ProjectID: "p", ID: id, Title: id, Status: "active", SourcePath: "AGENTS.md"}); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func bindingPlan() trackplan.Plan {
	return trackplan.Plan{ProjectID: "p", ID: "plan", RoadmapNodeID: "node", TaskID: "task", Title: "Work", Summary: "Work", Status: "proposed", Creator: "human", Items: []trackplan.Item{{ID: "one", Position: 1, Title: "One", Status: "pending"}}}
}

func bindingCount(t *testing.T, a *App, table string) int {
	t.Helper()
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPlanTaskBindingBothCreationOrders(t *testing.T) {
	for _, planFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("plan-first=%t", planFirst), func(t *testing.T) {
			a := bindingApp(t)
			ctx := context.Background()
			candidate := fenceCheckpoint(t, a, "task", lease.Claim{})
			candidate.RequestKey = "checkpoint-once"
			savePlan := func() {
				t.Helper()
				if _, err := a.AppendPlan(ctx, bindingPlan()); err != nil {
					t.Fatal(err)
				}
			}
			saveCheckpoint := func() {
				t.Helper()
				if _, err := a.SaveCheckpoint(ctx, candidate); err != nil {
					t.Fatal(err)
				}
			}
			if planFirst {
				savePlan()
				if got := bindingCount(t, a, "roadmap_task_bindings"); got != 0 {
					t.Fatalf("premature binding: %d", got)
				}
				saveCheckpoint()
			} else {
				saveCheckpoint()
				savePlan()
			}
			if got := bindingCount(t, a, "roadmap_task_bindings"); got != 1 {
				t.Fatalf("bindings after both records=%d", got)
			}
			// Retrying a checkpoint or recording another plan revision creates no duplicate link.
			saveCheckpoint()
			savePlan()
			if got := bindingCount(t, a, "roadmap_task_bindings"); got != 1 {
				t.Fatalf("bindings=%d", got)
			}
			inventory, err := a.Roadmap.TrackInventory(ctx, "p", roadmap.TrackInventoryOptions{RoadmapNodeID: "node"})
			if err != nil {
				t.Fatal(err)
			}
			if len(inventory.Items) != 1 || inventory.Items[0].SelectedTaskID != "task" || inventory.Items[0].BindingState != "explicit" {
				t.Fatalf("inventory: %+v", inventory)
			}
		})
	}
}

func TestCheckpointBindingUsesOperativePlan(t *testing.T) {
	a := bindingApp(t)
	ctx := context.Background()
	p := bindingPlan()
	p.Status = "active"
	if _, err := a.AppendPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Status = "proposed"
	p.TaskID = "replacement"
	p.RoadmapNodeID = "other"
	if _, err := a.AppendPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "replacement", lease.Claim{})); err != nil {
		t.Fatal(err)
	}
	if got := bindingCount(t, a, "roadmap_task_bindings"); got != 0 {
		t.Fatalf("replacement proposal bound: %d", got)
	}
	if _, err := a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "task", lease.Claim{})); err != nil {
		t.Fatal(err)
	}
	var node, task string
	if err := a.DB.QueryRow(`SELECT roadmap_node_id,task_id FROM roadmap_task_bindings`).Scan(&node, &task); err != nil {
		t.Fatal(err)
	}
	if node != "node" || task != "task" {
		t.Fatalf("bound %s/%s", node, task)
	}
	if _, err := a.AcceptPlan(ctx, "plan", "active", "reviewer", lease.Claim{}); err != nil {
		t.Fatal(err)
	}
	if got := bindingCount(t, a, "roadmap_task_bindings"); got != 2 {
		t.Fatalf("review did not bind replacement: %d", got)
	}
}

func TestCheckpointBindingPreservesSupersession(t *testing.T) {
	for _, supersedePlan := range []bool{false, true} {
		t.Run(fmt.Sprintf("plan=%t", supersedePlan), func(t *testing.T) {
			a := bindingApp(t)
			ctx := context.Background()
			p := bindingPlan()
			if supersedePlan {
				p.Status = "superseded"
			}
			if _, err := a.AppendPlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			first, err := a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "task", lease.Claim{}))
			if err != nil {
				t.Fatal(err)
			}
			if supersedePlan {
				if got := bindingCount(t, a, "roadmap_task_bindings"); got != 0 {
					t.Fatalf("superseded plan bound: %d", got)
				}
				return
			}
			_, err = a.Roadmap.AppendTaskBinding(ctx, roadmap.TaskBinding{ProjectID: "p", RoadmapNodeID: "node", TaskID: "task", Status: "superseded", Reason: "Reviewed removal", RequestKey: "remove-link"})
			if err != nil {
				t.Fatal(err)
			}
			cp := fenceCheckpoint(t, a, "task", lease.Claim{})
			cp.PredecessorCheckpointID = first.Checkpoint.ID
			if _, err := a.SaveCheckpoint(ctx, cp); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := a.DB.QueryRow(`SELECT status FROM roadmap_task_bindings ORDER BY revision DESC LIMIT 1`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "superseded" || bindingCount(t, a, "roadmap_task_bindings") != 2 {
				t.Fatalf("revived removed binding: %s", status)
			}
		})
	}
}

func TestPlanBindingFailureRollsBackWholeOperation(t *testing.T) {
	for _, operation := range []string{"append", "accept", "checkpoint"} {
		t.Run(operation, func(t *testing.T) {
			a := bindingApp(t)
			ctx := context.Background()
			p := bindingPlan()
			if operation != "checkpoint" {
				if _, err := a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "task", lease.Claim{})); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "accept" {
				if _, err := a.TrackPlans.AppendPlan(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			if operation == "checkpoint" {
				if _, err := a.AppendPlan(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
			tables := []string{"track_plan_revisions", "track_plan_items", "checkpoints", "checkpoint_actions", "checkpoint_action_transitions", "roadmap_task_bindings", "lease_stamps"}
			// A failed automatic link must not leave the parent write, actions or lease stamps behind.
			before := map[string]int{}
			for _, table := range tables {
				before[table] = bindingCount(t, a, table)
			}
			if _, err := a.DB.Exec(`CREATE TRIGGER reject_binding BEFORE INSERT ON roadmap_task_bindings BEGIN SELECT RAISE(ABORT,'injected binding failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "append":
				_, err = a.AppendPlan(ctx, p)
			case "accept":
				_, err = a.AcceptPlan(ctx, "plan", "active", "reviewer", lease.Claim{})
			case "checkpoint":
				_, err = a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "task", lease.Claim{}))
			}
			if err == nil || !strings.Contains(err.Error(), "injected binding failure") || !strings.Contains(err.Error(), "plan") {
				t.Fatalf("binding failure hidden: %v", err)
			}
			for _, table := range tables {
				if got := bindingCount(t, a, table); got != before[table] {
					t.Fatalf("partial %s: %s rows=%d before=%d", operation, table, got, before[table])
				}
			}
			if _, err := a.DB.Exec(`DROP TRIGGER reject_binding`); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "append":
				_, err = a.AppendPlan(ctx, p)
			case "accept":
				_, err = a.AcceptPlan(ctx, "plan", "active", "reviewer", lease.Claim{})
			case "checkpoint":
				_, err = a.SaveCheckpoint(ctx, fenceCheckpoint(t, a, "task", lease.Claim{}))
			}
			if err != nil {
				t.Fatalf("retry after rollback: %v", err)
			}
			if got := bindingCount(t, a, "roadmap_task_bindings"); got != 1 {
				t.Fatalf("retry bindings=%d", got)
			}
		})
	}
}

func TestPlanBindingRejectsUnknownNodeBeforeTaskExists(t *testing.T) {
	a := bindingApp(t)
	p := bindingPlan()
	p.RoadmapNodeID = "missing"
	if _, err := a.AppendPlan(context.Background(), p); err == nil || !strings.Contains(err.Error(), `roadmap node "missing"`) {
		t.Fatalf("unknown node: %v", err)
	}
	if got := bindingCount(t, a, "track_plan_revisions"); got != 0 {
		t.Fatalf("invalid plan saved: %d", got)
	}
}

// Keep multiple explicit declarations attributable rather than choosing a node
// by recency, title or branch name when the task receives its first checkpoint.
func TestCheckpointCompletesAllPendingPlanDeclarations(t *testing.T) {
	a := bindingApp(t)
	ctx := context.Background()
	p := bindingPlan()
	if _, err := a.AppendPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.ID = "second"
	p.RoadmapNodeID = "other"
	if _, err := a.AppendPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "task", Task: "Task", NextAction: "Continue"}); err != nil {
		t.Fatal(err)
	}
	if got := bindingCount(t, a, "roadmap_task_bindings"); got != 2 {
		t.Fatalf("bindings=%d", got)
	}
}
