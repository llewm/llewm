package workstate

import (
	"context"
	"reflect"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/projectpulse"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

type fakePulse struct{ value projectpulse.Pulse }

func (fake fakePulse) Next(context.Context, string) (projectpulse.Pulse, error) {
	return fake.value, nil
}

type fakeInventory struct {
	values map[string]roadmap.TrackInventory
}

func (fake fakeInventory) TrackInventory(_ context.Context, _ string, options roadmap.TrackInventoryOptions) (roadmap.TrackInventory, error) {
	if options.RoadmapNodeID != "" {
		return fake.values["node:"+options.RoadmapNodeID], nil
	}
	if options.TaskID != "" {
		return fake.values["task:"+options.TaskID], nil
	}
	return fake.values[options.Filter], nil
}

type fakePlans struct{ ready []trackplan.ReadyItem }

func (fake fakePlans) Ready(context.Context, string, trackplan.PlanListOptions) ([]trackplan.ReadyItem, error) {
	return fake.ready, nil
}

func TestSummaryCombinesCurrentWorkReadyItemsAndFindings(t *testing.T) {
	checkpointValue := &checkpoint.Checkpoint{ID: "checkpoint-a", TaskID: "task-a", Task: "Current work", Status: "in_progress"}
	item := roadmap.TrackInventoryItem{
		Node: &roadmap.NodeRef{ID: "node-a", Title: "Current work", Status: "active"}, NodeRevision: 2,
		SelectedTaskID: "task-a", LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "checkpoint-a", Status: "in_progress"},
		Active: true, Resumable: true, Anomalies: []string{"stale_task_binding"},
	}
	service := Service{
		Pulse:   fakePulse{value: projectpulse.Pulse{Checkpoint: checkpointValue, RecommendedNext: "Continue", RoutingDecision: "project_latest", RoadmapStatus: roadmap.Status{Freshness: "current"}}},
		Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"current_focus": {Total: 1, Items: []roadmap.TrackInventoryItem{item}}}},
		Plans:   fakePlans{ready: []trackplan.ReadyItem{{PlanID: "plan", Item: trackplan.Item{ID: "next", Title: "Next item"}}}},
	}
	result, err := service.Summary(context.Background(), "project", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checkpoint == nil || result.Checkpoint.ID != "checkpoint-a" || result.Counts.Active != 1 || result.Counts.Resumable != 1 || result.Counts.Anomalous != 1 || result.Counts.Ready != 1 || len(result.Findings) != 1 || result.Findings[0].Code != "stale_task_binding" {
		t.Fatalf("unexpected summary: %#v", result)
	}
}

func TestCheckReportsTerminalOpenActionWithoutRepair(t *testing.T) {
	item := roadmap.TrackInventoryItem{
		Node: &roadmap.NodeRef{ID: "node", Status: "completed"}, NodeRevision: 3, SelectedTaskID: "task",
		LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "done", Status: "completed"}, UnresolvedActionCount: 1,
	}
	service := Service{Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"all": {Items: []roadmap.TrackInventoryItem{item}}, "stale": {Total: 1, Items: []roadmap.TrackInventoryItem{item}}}}}
	result, err := service.Check(context.Background(), "project", "all", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "findings" || result.Total != 1 || result.Findings[0].Code != "terminal_task_open_action" || result.Findings[0].CheckpointID != "done" {
		t.Fatalf("unexpected check: %#v", result)
	}
	stale, err := service.List(context.Background(), "project", "stale", 20)
	if err != nil || stale.InventoryTotal != 1 || len(stale.Items) != 1 {
		t.Fatalf("terminal open action was omitted from stale view: %#v err=%v", stale, err)
	}
}

func TestCheckReportsActiveRoadmapWithTerminalSelectedTask(t *testing.T) {
	item := roadmap.TrackInventoryItem{
		Node: &roadmap.NodeRef{ID: "active-node", Status: "active"}, NodeRevision: 4, SelectedTaskID: "selected-task",
		LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "completed-tip", Status: "completed"},
	}
	service := Service{Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"all": {Items: []roadmap.TrackInventoryItem{item}}}}}
	result, err := service.Check(context.Background(), "project", "all", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "findings" || result.Total != 1 || result.Findings[0].Code != "roadmap_terminal_task" {
		t.Fatalf("unexpected check: %#v", result)
	}
	wantRefs := []string{"roadmap:active-node@4", "task:selected-task", "checkpoint:completed-tip"}
	if !reflect.DeepEqual(result.Findings[0].SourceRefs, wantRefs) {
		t.Fatalf("unexpected source references: %#v", result.Findings[0].SourceRefs)
	}
}

func TestCheckBoundsFindingsIndependentlyFromInventoryItems(t *testing.T) {
	item := roadmap.TrackInventoryItem{
		Node:             &roadmap.NodeRef{ID: "node", Status: "active"},
		SelectedTaskID:   "task",
		LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "done", Status: "completed"},
		Anomalies:        []string{"roadmap_checkpoint_status"},
	}
	service := Service{Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"all": {Items: []roadmap.TrackInventoryItem{item}}}}}
	result, err := service.Check(context.Background(), "project", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Findings) != 1 || !result.Truncated || result.Findings[0].Code != "roadmap_checkpoint_status" {
		t.Fatalf("unexpected bounded check: %#v", result)
	}
}

func TestCheckRejectsUnknownScope(t *testing.T) {
	service := Service{}
	if _, err := service.Check(context.Background(), "project", "future", 20); err == nil {
		t.Fatal("unknown scope was accepted")
	}
}

func TestListMapsOperationalViewsAndIncludesReadyPlanItems(t *testing.T) {
	item := roadmap.TrackInventoryItem{Node: &roadmap.NodeRef{ID: "node-a"}, SelectedTaskID: "task-a", Resumable: true}
	service := Service{
		Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"resumable": {Total: 1, Items: []roadmap.TrackInventoryItem{item}}}},
		Plans:   fakePlans{ready: []trackplan.ReadyItem{{PlanID: "plan", Item: trackplan.Item{ID: "step"}}}},
	}
	result, err := service.List(context.Background(), "project", "next", 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.View != "next" || result.InventoryTotal != 1 || len(result.Items) != 1 || result.ReadyTotal != 1 || len(result.ReadyItems) != 1 {
		t.Fatalf("unexpected list: %#v", result)
	}
	if _, err := service.List(context.Background(), "project", "eventually", 20); err == nil {
		t.Fatal("unknown work view was accepted")
	}
}

func TestShowMatchesExactNodeAndTaskWithoutDuplicatingSameTrack(t *testing.T) {
	item := roadmap.TrackInventoryItem{Node: &roadmap.NodeRef{ID: "shared"}, SelectedTaskID: "shared", BindingState: "explicit"}
	inventory := roadmap.TrackInventory{Total: 1, Items: []roadmap.TrackInventoryItem{item}}
	service := Service{Roadmap: fakeInventory{values: map[string]roadmap.TrackInventory{"node:shared": inventory, "task:shared": inventory}}}
	result, err := service.Show(context.Background(), "project", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchedBy != "roadmap_node_and_task" || result.Item.Node == nil || result.Item.Node.ID != "shared" {
		t.Fatalf("unexpected detail: %#v", result)
	}
	if _, err := service.Show(context.Background(), "project", "missing"); err == nil {
		t.Fatal("missing work identifier was accepted")
	}
}
