package roadmap

import (
	"slices"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func TestTaskBindingRequiredOnlyForWorkUnderway(t *testing.T) {
	for _, status := range []string{"proposed", "accepted", "completed", "deferred", "active", "blocked"} {
		t.Run(status, func(t *testing.T) {
			ctx, service, projectID, _, _ := testService(t)
			if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "track", Title: "Track", Status: status, SourcePath: "ROADMAP.md"}); err != nil {
				t.Fatal(err)
			}
			inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all"})
			if err != nil || len(inventory.Items) != 1 {
				t.Fatalf("inventory: %+v %v", inventory, err)
			}
			item := inventory.Items[0]
			wantAnomaly := status == "active" || status == "blocked"
			if item.BindingState != "missing" || hasString(item.Anomalies, "missing_task_binding") != wantAnomaly {
				t.Fatalf("binding state: %+v", item)
			}
			stale, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "stale"})
			// The stale filter is limited to actionable/current work; an
			// unstarted blocked track retains its finding in the blocked view.
			if err != nil || (stale.Total == 1) != (status == "active") {
				t.Fatalf("stale view: %+v %v", stale, err)
			}
		})
	}
}

func TestTrackInventoryJoinsBindingsTasksActionsAndAnomalies(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node-a", Title: "Active work", Status: "active", Priority: 10, SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node-b", Title: "Accepted work", Status: "accepted", Priority: 20, SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: service.DB}
	if _, err := checkpoints.TransitionTask(ctx, checkpoint.TaskTransition{ProjectID: projectID, Operation: "start", TaskID: "task-a", RequestKey: "start-task-a"}); err != nil {
		t.Fatal(err)
	}
	created, err := checkpoints.CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "task-a", Task: "Implement active work", Status: "in_progress", NextAction: "Continue the inventory"})
	if err != nil {
		t.Fatal(err)
	}
	if !created.ActionCreated {
		t.Fatal("expected unresolved checkpoint action")
	}
	if _, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "orphan-task", Task: "Unbound legacy task", Status: "paused", NextAction: "Resume later"}); err != nil {
		t.Fatal(err)
	}
	bound, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: "node-a", TaskID: "task-a", Status: "active", Reason: "IDs differ", RequestKey: "bind-node-a-task-a"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.Revision != 1 || bound.RoadmapNodeRevision != 1 || bound.Authority != "caller_asserted" {
		t.Fatalf("unexpected binding: %#v", bound)
	}
	replayed, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: "node-a", TaskID: "task-a", Status: "active", Reason: "IDs differ", RequestKey: "bind-node-a-task-a"})
	if err != nil || replayed.Revision != bound.Revision {
		t.Fatalf("binding replay failed: %#v %v", replayed, err)
	}

	inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", Limit: 10, RefLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 3 || inventory.Truncated || len(inventory.Items) != 3 {
		t.Fatalf("unexpected inventory bounds: %#v", inventory)
	}
	active := inventory.Items[0]
	if active.Node == nil || active.Node.ID != "node-a" || active.SelectedTaskID != "task-a" || active.BindingState != "explicit" || !active.Active || !active.Resumable || active.UnresolvedActionCount != 1 || len(active.Actions) != 1 || active.LatestCheckpoint == nil || active.LatestCheckpoint.ID != created.Checkpoint.ID {
		t.Fatalf("unexpected active joined item: %#v", active)
	}
	if !hasString(active.Anomalies, "missing_track_plan") {
		t.Fatalf("explicitly bound node without a plan: %#v", active.Anomalies)
	}
	accepted := inventory.Items[1]
	if accepted.Node == nil || accepted.Node.ID != "node-b" || accepted.BindingState != "missing" || !accepted.Resumable || !hasString(accepted.ResumableReasons, "accepted_roadmap_work_without_task") || hasString(accepted.Anomalies, "missing_task_binding") {
		t.Fatalf("unexpected accepted item: %#v", accepted)
	}
	orphan := inventory.Items[2]
	if orphan.Node != nil || orphan.SelectedTaskID != "orphan-task" || !hasString(orphan.Anomalies, "task_without_roadmap_node") {
		t.Fatalf("unexpected unbound task: %#v", orphan)
	}

	paged, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "anomalous", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if paged.Total != 2 || len(paged.Items) != 1 || paged.Omitted != 1 || !paged.Truncated {
		t.Fatalf("unexpected paged inventory: %#v", paged)
	}
	exact, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", TaskID: "task-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Total != 1 || len(exact.Items) != 1 || exact.Items[0].SelectedTaskID != "task-a" {
		t.Fatalf("unexpected task-scoped inventory: %#v", exact)
	}
	exactNode, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", RoadmapNodeID: "node-a", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if exactNode.Total != 1 || len(exactNode.Items) != 1 || exactNode.Items[0].Node == nil || exactNode.Items[0].Node.ID != "node-a" {
		t.Fatalf("unexpected node-scoped inventory: %#v", exactNode)
	}
	stale, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "stale", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// node-a is active with an explicit binding but no plan, so it is stale too.
	if stale.Total != 1 || stale.Items[0].Node == nil || stale.Items[0].Node.ID != "node-a" {
		t.Fatalf("unexpected stale inventory: %#v", stale)
	}
}

func TestTrackInventoryPreservesAmbiguousBindingsUntilExplicitlySuperseded(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "node", Title: "Node", Status: "active", Priority: 5, SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: service.DB}
	for _, taskID := range []string{"task-a", "task-b"} {
		if _, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: taskID, Task: taskID, Status: "paused", NextAction: "Continue"}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: "node", TaskID: taskID, Status: "active", RequestKey: "bind-node-" + taskID}); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "anomalous"})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 1 || inventory.Items[0].BindingState != "ambiguous" || inventory.Items[0].SelectedTaskID != "" || !hasString(inventory.Items[0].Anomalies, "ambiguous_task_binding") {
		t.Fatalf("ambiguity was hidden: %#v", inventory)
	}
	if _, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: "node", TaskID: "task-b", Status: "superseded", Revision: 2, Reason: "Reviewed correction", RequestKey: "supersede-node-task-b"}); err != nil {
		t.Fatal(err)
	}
	inventory, err = service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Items[0].BindingState != "explicit" || inventory.Items[0].SelectedTaskID != "task-a" {
		t.Fatalf("explicit supersession did not resolve pair: %#v", inventory.Items[0])
	}
}

func TestTrackInventoryExplicitBindingSelectsOverLegacySameID(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "switching", Title: "Switching", Status: "completed", Priority: 5, SourcePath: "docs/roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: service.DB}
	for _, taskID := range []string{"switching", "gate1-final-review"} {
		if _, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: taskID, Task: taskID, Status: "completed", NextAction: "Complete"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: "switching", TaskID: "gate1-final-review", Status: "active", RequestKey: "reviewed-gate1-final-review"}); err != nil {
		t.Fatal(err)
	}
	inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Total != 1 || inventory.Items[0].BindingState != "explicit" || inventory.Items[0].SelectedTaskID != "gate1-final-review" || len(inventory.Items[0].Tasks) != 2 || hasString(inventory.Items[0].Anomalies, "ambiguous_task_binding") {
		t.Fatalf("explicit selection did not retain legacy task for audit: %#v", inventory)
	}
}

func TestCompletedNodeAcceptedItemDoesNotResumeFinishedTask(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	if _, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "done", Title: "Done", Status: "completed", SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (checkpoint.Service{DB: service.DB}).Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "done", Task: "Done", Status: "completed", NextAction: "Await work"}); err != nil {
		t.Fatal(err)
	}
	items := workitem.Service{DB: service.DB}
	if _, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "future", ProjectID: projectID, Kind: "idea", Title: "Future", Body: "Separate work", RoadmapNodeID: "done", Creator: "human"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Dispose(ctx, workitem.Disposition{ProjectID: projectID, ItemID: "future", Status: "accepted", Reason: "Do later", TargetRefs: []string{"task:new-task"}, Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Items) != 1 || inventory.Items[0].Resumable || inventory.Items[0].AcceptedWorkItems != 1 {
		t.Fatalf("accepted follow-up revived completed task: %#v", inventory.Items)
	}
}

func hasString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestTrackInventorySelectsTheOpenTaskAndHidesFinishedUnboundTasks(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	for _, node := range []Node{{ID: "node-a", Status: "active", Priority: 10}, {ID: "node-d", Status: "deferred", Priority: 20}} {
		node.ProjectID, node.Title, node.SourcePath = projectID, node.ID, "docs/roadmap.md"
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	checkpoints := checkpoint.Service{DB: service.DB}
	for _, value := range []checkpoint.Checkpoint{
		{TaskID: "old-a", Task: "Old", Status: "completed", NextAction: "None"},
		{TaskID: "new-a", Task: "New", Status: "in_progress", NextAction: "Continue"},
		{TaskID: "deferred-work", Task: "Deferred", Status: "in_progress", NextAction: "Continue"},
		{TaskID: "finished-loose", Task: "Loose", Status: "completed", NextAction: "None"},
		{TaskID: "open-loose", Task: "Loose", Status: "paused", NextAction: "Resume"},
	} {
		value.ProjectID = projectID
		if _, err := checkpoints.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	for task, node := range map[string]string{"old-a": "node-a", "new-a": "node-a", "deferred-work": "node-d"} {
		if _, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: node, TaskID: task, Status: "active", Reason: "test", RequestKey: "bind-" + task}); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]TrackInventoryItem{}
	for _, item := range inventory.Items {
		key := item.SelectedTaskID
		if item.Node != nil {
			key = item.Node.ID
		}
		byKey[key] = item
	}
	if a := byKey["node-a"]; a.SelectedTaskID != "new-a" || a.BindingState != "explicit" || hasString(a.Anomalies, "ambiguous_task_binding") {
		t.Fatalf("node with a finished and an open task: %#v", a)
	}
	if d := byKey["node-d"]; !hasString(d.Anomalies, "work_on_inactive_node") {
		t.Fatalf("open work on a deferred node not flagged: %#v", d.Anomalies)
	}
	if _, ok := byKey["finished-loose"]; ok {
		t.Fatal("all listed a finished unbound task")
	}
	if _, ok := byKey["open-loose"]; !ok || inventory.Total != 3 {
		t.Fatalf("all lost an open unbound task: total %d", inventory.Total)
	}
	everything, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "everything", Limit: 10})
	if err != nil || everything.Total != 4 {
		t.Fatalf("everything: total %d err %v", everything.Total, err)
	}
	oldTip, err := checkpoints.LatestSelected(ctx, projectID, checkpoint.Selection{TaskID: "old-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "old-a", PredecessorCheckpointID: oldTip.ID, Task: "Old", Status: "in_progress", NextAction: "Reopened"}); err != nil {
		t.Fatal(err)
	}
	again, _ := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "all", RoadmapNodeID: "node-a", Limit: 10})
	if len(again.Items) != 1 || again.Items[0].BindingState != "ambiguous" {
		t.Fatalf("two open tasks on one node: %#v", again.Items)
	}
	if bound, err := service.EnsureTaskBinding(ctx, projectID, "node-d", "open-loose", "plan", "ensure-1"); err != nil || !bound {
		t.Fatalf("ensure binding: %v %v", bound, err)
	}
	if bound, err := service.EnsureTaskBinding(ctx, projectID, "node-d", "open-loose", "plan", "ensure-2"); err != nil || bound {
		t.Fatalf("ensure binding twice: %v %v", bound, err)
	}
	if bound, err := service.EnsureTaskBinding(ctx, projectID, "node-d", "no-such-task", "plan", "ensure-3"); err != nil || bound {
		t.Fatalf("ensure binding for an unrecorded task: %v %v", bound, err)
	}
}

// Exercise the real join and page boundary: neither a high numeric priority
// nor a terminal track's stale checkpoint may bury current/open work.
func TestTrackInventoryOpenOrderingBeforePagination(t *testing.T) {
	ctx, service, projectID, _, _ := testService(t)
	for _, node := range []Node{
		{ID: "finished", Title: "A finished track", Status: "completed", Priority: 0},
		{ID: "replaced", Title: "A replaced track", Status: "superseded", Priority: 0},
		{ID: "deferred", Title: "Deferred", Status: "deferred", Priority: 0},
		{ID: "proposal-z", Title: "alpha", Status: "proposed", Priority: 10},
		{ID: "proposal-a", Title: "Beta", Status: "proposed", Priority: 10},
		{ID: "proposal-first", Title: "Zulu", Status: "proposed", Priority: 1},
		{ID: "proposal-tie", Title: "ALPHA", Status: "proposed", Priority: 10},
		{ID: "ready", Title: "Next work", Status: "accepted", Priority: 80},
		{ID: "blocked", Title: "Blocked work", Status: "blocked", Priority: 90},
		{ID: "active", Title: "Active work", Status: "active", Priority: 100},
	} {
		node.ProjectID, node.SourcePath = projectID, "ROADMAP.md"
		if _, err := service.AppendNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	checkpoints := checkpoint.Service{DB: service.DB}
	for _, cp := range []checkpoint.Checkpoint{
		{TaskID: "finished", Task: "Stale unfinished checkpoint", Status: "in_progress"},
		{TaskID: "replaced", Task: "Superseded track checkpoint", Status: "in_progress"},
		{TaskID: "ready", Task: "Finished precursor", Status: "completed"},
		{TaskID: "loose-finished", Task: "Finished task", Status: "completed"},
		{TaskID: "loose-paused", Task: "Paused task", Status: "paused"},
		{TaskID: "loose-blocked", Task: "Blocked task", Status: "blocked"},
		{TaskID: "loose-active", Task: "Other active task", Status: "in_progress"},
		{TaskID: "current", Task: "Current unbound task", Status: "in_progress"},
	} {
		cp.ProjectID, cp.NextAction = projectID, "Continue if open"
		if _, err := checkpoints.Create(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}
	wantOpen := []string{"current", "active", "loose-active", "blocked", "loose-blocked", "ready", "proposal-first", "proposal-tie", "proposal-z", "proposal-a", "deferred", "loose-paused"}
	ids := func(items []TrackInventoryItem) []string {
		result := []string{}
		for _, item := range items {
			id := item.SelectedTaskID
			if item.Node != nil {
				id = item.Node.ID
			}
			result = append(result, id)
		}
		return result
	}
	for _, filter := range []string{"open", "all", "everything"} {
		want := slices.Clone(wantOpen)
		if filter != "open" {
			want = append(want, "finished", "replaced")
		}
		if filter == "everything" {
			want = append(want, "loose-finished")
		}
		all, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: filter, Limit: 100})
		if err != nil || !slices.Equal(ids(all.Items), want) {
			t.Fatalf("%s: got %v want %v err %v", filter, ids(all.Items), want, err)
		}
		var paged []string
		for offset := 0; offset < len(want); offset += 3 {
			page, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: filter, Limit: 3, Offset: offset})
			if err != nil || page.Total != len(want) {
				t.Fatalf("%s page %d: %+v %v", filter, offset, page, err)
			}
			paged = append(paged, ids(page.Items)...)
		}
		if !slices.Equal(paged, want) {
			t.Fatalf("%s paged: %v", filter, paged)
		}
	}
	// A completed latest checkpoint is history, not current work to promote.
	if _, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "last-finished", Task: "Last finished", Status: "completed", NextAction: "Done"}); err != nil {
		t.Fatal(err)
	}
	first, err := service.TrackInventory(ctx, projectID, TrackInventoryOptions{Filter: "everything", Limit: 1})
	if err != nil || !slices.Equal(ids(first.Items), []string{"active"}) {
		t.Fatalf("finished current promoted: %+v %v", first, err)
	}
}
