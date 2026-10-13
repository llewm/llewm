package trackplan

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func testService(t *testing.T) (context.Context, Service, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := activity.Store{DB: db}
	first, err := store.EnsureProject(ctx, "/first", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureProject(ctx, "/second", "second")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Now: func() time.Time { return now }}, first, second
}

func TestPlanRevisionHistoryAndReadyItems(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	source, err := workitem.Service{DB: service.DB}.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: "plan-source", ProjectID: projectID, Kind: "idea", Title: "Persist the plan", Body: "Resume without rederiving", Creator: "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	base := Plan{ProjectID: projectID, ID: "track-continuity", RoadmapNodeID: "continuity-track-continuity", TaskID: "continuity-track-continuity", Title: "Track continuity rollout", Summary: "Persist and expose the reviewed steps", Status: "accepted", Creator: "human-review", WorkItemIDs: []string{"work_item:" + source.ID}, Items: []Item{
		{ID: "schema", Position: 0, Title: "Persist schema", Status: "completed", WorkItemIDs: []string{source.ID}},
		{ID: "reads", Position: 1, Title: "Expose reads", Status: "pending", DependsOn: []string{"schema"}},
		{ID: "routing", Position: 2, Title: "Update routing", Status: "pending", DependsOn: []string{"reads"}},
	}}
	first, err := service.AppendPlan(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 {
		t.Fatalf("unexpected revision: %#v", first)
	}
	ready, err := service.Ready(ctx, projectID, PlanListOptions{})
	if err != nil || len(ready) != 1 || ready[0].Item.ID != "reads" {
		t.Fatalf("unexpected ready items: %#v %v", ready, err)
	}
	base.Revision = 0
	base.Items[1].Status = "completed"
	second, err := service.AppendPlan(ctx, base)
	if err != nil || second.Revision != 2 {
		t.Fatalf("second revision failed: %#v %v", second, err)
	}
	ready, err = service.Ready(ctx, projectID, PlanListOptions{})
	if err != nil || len(ready) != 1 || ready[0].Item.ID != "routing" {
		t.Fatalf("updated ready items mismatch: %#v %v", ready, err)
	}
	history, err := service.PlanHistory(ctx, projectID, base.ID)
	if err != nil || len(history) != 2 || len(history[0].WorkItemIDs) != 1 || history[0].WorkItemIDs[0] != source.ID || history[0].Items[1].Status != "pending" || history[1].Items[1].Status != "completed" {
		t.Fatalf("revision history mismatch: %#v %v", history, err)
	}
	if _, err := service.AppendPlan(ctx, Plan{ProjectID: projectID, ID: "model-plan", RoadmapNodeID: "node", Title: "Model plan", Summary: "Not accepted", Status: "accepted", Creator: "agent", Model: "gpt", Items: []Item{{ID: "one", Position: 0, Title: "One", Status: "pending"}}}); err == nil {
		t.Fatal("model-suggested accepted plan succeeded")
	}
	if _, err := service.AppendPlan(ctx, Plan{ProjectID: projectID, ID: "cycle", RoadmapNodeID: "node", Title: "Cycle", Summary: "Invalid dependencies", Status: "proposed", Creator: "human", Items: []Item{{ID: "one", Position: 0, Title: "One", Status: "pending", DependsOn: []string{"two"}}, {ID: "two", Position: 1, Title: "Two", Status: "pending", DependsOn: []string{"one"}}}}); err == nil {
		t.Fatal("dependency cycle succeeded")
	}
}

func TestPlanListPageReportsExactBounds(t *testing.T) {
	ctx, service, project, _ := testService(t)
	for _, id := range []string{"plan-a", "plan-b"} {
		if _, err := service.AppendPlan(ctx, Plan{ProjectID: project, ID: id, RoadmapNodeID: "node", Title: id, Summary: "bounded list", Status: "proposed", Creator: "human", Items: []Item{{ID: "item", Position: 0, Title: "Item", Status: "pending"}}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ListPlansPage(ctx, project, PlanListOptions{Status: "proposed", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Plans) != 1 || page.Total != 2 || page.Limit != 1 || !page.Truncated || page.Plans[0].ID != "plan-a" {
		t.Fatalf("unexpected plan page: %#v", page)
	}
	empty, err := service.ListPlansPage(ctx, project, PlanListOptions{Status: "completed", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Plans) != 0 || empty.Total != 0 || empty.Truncated {
		t.Fatalf("unexpected empty plan page: %#v", empty)
	}
}

func TestPlanHistoryPageBoundsMetadataAndReportsEmptyOffsets(t *testing.T) {
	ctx, service, project, _ := testService(t)
	for revision := 1; revision <= 3; revision++ {
		plan := Plan{ProjectID: project, ID: "history", RoadmapNodeID: "node-a", Title: "Revision", Summary: "private summary detail", Status: "proposed", Creator: "human", Items: []Item{{ID: "item", Position: 0, Title: "Private item detail", Detail: "body", Status: "pending"}}}
		if revision > 1 {
			plan.Revision = 0
		}
		if _, err := service.AppendPlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.PlanHistoryPage(ctx, project, "plan:history", PlanHistoryOptions{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Limit != 1 || page.Offset != 1 || !page.HasMore || len(page.Revisions) != 1 || page.Revisions[0].Revision != 2 || page.Revisions[0].ItemCount != 1 {
		t.Fatalf("unexpected history page: %#v", page)
	}
	end, err := service.PlanHistoryPage(ctx, project, "history", PlanHistoryOptions{Limit: 1, Offset: 3})
	if err != nil || end.Total != 3 || len(end.Revisions) != 0 || end.HasMore {
		t.Fatalf("unexpected end history page: %#v %v", end, err)
	}
	empty, err := service.PlanHistoryPage(ctx, project, "missing", PlanHistoryOptions{Limit: 1})
	if err != nil || empty.Total != 0 || len(empty.Revisions) != 0 || empty.HasMore {
		t.Fatalf("unexpected empty history page: %#v %v", empty, err)
	}
	if _, err := service.PlanHistoryPage(ctx, project, "history", PlanHistoryOptions{Limit: 101}); err == nil {
		t.Fatal("history limit above 100 was accepted")
	}
	if _, err := service.PlanHistoryPage(ctx, project, "history", PlanHistoryOptions{Offset: -1}); err == nil {
		t.Fatal("negative history offset was accepted")
	}
}

func TestAcceptRecordsHumanReviewOfLatestProposal(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	proposal := Plan{ProjectID: projectID, ID: "plan-a", RoadmapNodeID: "node-a", Title: "Plan", Summary: "Plan summary", Status: "proposed", Creator: "agent", Model: "model-x", Items: []Item{{ID: "one", Position: 1, Title: "One", Status: "completed"}, {ID: "two", Position: 2, Title: "Two", Status: "pending"}}}
	if _, err := service.AppendPlan(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ status, reviewer string }{{"proposed", "owner"}, {"accepted", " "}} {
		if _, err := service.Accept(ctx, projectID, "plan-a", bad.status, bad.reviewer, lease.Fence{}); err == nil {
			t.Fatalf("accepted status %q reviewer %q", bad.status, bad.reviewer)
		}
	}
	if _, err := service.Accept(ctx, projectID, "plan-a", "completed", "owner", lease.Fence{}); err == nil || !strings.Contains(err.Error(), "item two is pending") {
		t.Fatalf("completed with open items: %v", err)
	}
	accepted, err := service.Accept(ctx, projectID, "plan-a", "accepted", "owner", lease.Fence{})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Revision != 2 || accepted.Status != "accepted" || accepted.Creator != "owner" || accepted.Model != "" || accepted.Authority != "caller_asserted" || len(accepted.Items) != 2 || accepted.Summary != "Plan summary" {
		t.Fatalf("accepted = %+v", accepted)
	}
	if _, err = service.Accept(ctx, projectID, "plan-a", "accepted", "owner", lease.Fence{}); err == nil || !strings.Contains(err.Error(), "not proposed") {
		t.Fatalf("re-accepting a reviewed revision: %v", err)
	}
	if _, err = service.Accept(ctx, projectID, "missing", "accepted", "owner", lease.Fence{}); err == nil {
		t.Fatal("missing plan accepted")
	}
}
