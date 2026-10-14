package trackplan

import (
	"testing"
)

func approvedFixture(t *testing.T) (Service, Plan) {
	t.Helper()
	ctx, s, p, _ := testService(t)
	value, err := s.AppendPlan(ctx, Plan{ProjectID: p, ID: "agency", RoadmapNodeID: "track", Title: "Approved intent", Summary: "First slice", Status: "active", Creator: "person", Items: []Item{{ID: "one", Position: 0, Title: "Implement", Detail: "Bounded work", AcceptanceCriteria: "Checks pass", Status: "active"}, {ID: "two", Position: 1, Title: "Verify", Status: "pending", DependsOn: []string{"one"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, value
}
func modelNext(p Plan) Plan {
	p.BaseRevision = p.Revision
	p.Revision = 0
	p.Model = "test-model"
	p.Creator = "agent"
	p.Status = "proposed"
	p.Items = append([]Item{}, p.Items...)
	return p
}
func TestProgressKeepsBaselineAndUnblocksWithoutHumanReview(t *testing.T) {
	s, p := approvedFixture(t)
	next := modelNext(p)
	next.Items[0].Status = "completed"
	next.Items[0].ProgressNote = "Checks passed"
	next.Items[0].EvidenceRefs = []string{"commit:checked"}
	value, err := s.AppendPlan(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != "active" || value.Change.Kind != "progress" || value.Change.BaselineRevision != p.Revision {
		t.Fatalf("lost baseline: %#v", value)
	}
	ready, err := s.Ready(t.Context(), p.ProjectID, PlanListOptions{})
	if err != nil || len(ready) != 1 || ready[0].Item.ID != "two" {
		t.Fatal(ready, err)
	}
	original, _ := s.Plan(t.Context(), p.ProjectID, p.ID, p.Revision)
	if original.Items[0].Status != "active" {
		t.Fatal("rewrote observation")
	}
	if _, err := s.AppendPlan(t.Context(), next); err == nil {
		t.Fatal("accepted stale progress")
	}
}
func TestCompletionRequiresEvidenceAndNote(t *testing.T) {
	s, p := approvedFixture(t)
	next := modelNext(p)
	next.Items[0].Status = "completed"
	if _, err := s.AppendPlan(t.Context(), next); err == nil {
		t.Fatal("accepted unsupported completion")
	}
}
func TestScopeAndAcceptanceChangesNeedReviewByDefault(t *testing.T) {
	for _, change := range []string{"scope", "acceptance_criteria", "dependencies", "reorder_items", "remove_items"} {
		t.Run(change, func(t *testing.T) {
			s, p := approvedFixture(t)
			next := modelNext(p)
			switch change {
			case "scope":
				next.Items[0].Detail = "Expanded work"
			case "acceptance_criteria":
				next.Items[0].AcceptanceCriteria = "Skip checks"
			case "dependencies":
				next.Items[1].DependsOn = nil
			case "reorder_items":
				next.Items[0].Position = 2
			case "remove_items":
				next.Items[1].Status = "superseded"
			}
			v, err := s.AppendPlan(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			if v.Status != "proposed" || v.Authority != "model_suggested" || !oneOf(change, v.Change.Categories...) {
				t.Fatalf("autoaccepted intent change: %#v", v)
			}
			baseline, err := s.EffectivePlan(t.Context(), p.ProjectID, p.ID)
			if err != nil || baseline.Revision != p.Revision {
				t.Fatal("proposal displaced operative baseline", baseline, err)
			}
		})
	}
}
func TestDelegationBoundsAndProgressOverride(t *testing.T) {
	s, p := approvedFixture(t)
	s.Policy = Policy{Mode: "bounded", Allow: []string{"dependencies"}}
	next := modelNext(p)
	next.Items[1].DependsOn = nil
	v, err := s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "active" || v.Change.Kind != "delegated" {
		t.Fatal(v, err)
	}
	next = modelNext(v)
	next.Items[0].AcceptanceCriteria = "Weaker gate"
	v, err = s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "proposed" {
		t.Fatal(v, err)
	}
	s, p = approvedFixture(t)
	s.Policy = Policy{Mode: "agent", Progress: "review"}
	next = modelNext(p)
	next.Items[0].Status = "blocked"
	v, err = s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "proposed" {
		t.Fatal("progress review bypassed", v, err)
	}
	s.Policy = Policy{Mode: "agent"}
	next = modelNext(v)
	next.Items[0].Detail = "Expanded" // delegation compares against operative intent
	v, err = s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "active" || v.Change.Kind != "delegated" {
		t.Fatal(v, err)
	}
}

func TestProgressCanContinueBaselineWithProposalInHistory(t *testing.T) {
	s, p := approvedFixture(t)
	proposal := modelNext(p)
	proposal.Items[0].Detail = "Unreviewed expansion"
	pending, err := s.AppendPlan(t.Context(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	progress := modelNext(p)
	progress.BaseRevision = pending.Revision
	progress.Items[0].ProgressNote = "Original scope continues"
	progress.Items[0].Status = "blocked"
	v, err := s.AppendPlan(t.Context(), progress)
	if err != nil || v.Change.Kind != "progress" || v.Change.ComparisonRevision != p.Revision || v.Change.ParentRevision != pending.Revision {
		t.Fatal(v, err)
	}
	saved, _ := s.Plan(t.Context(), p.ProjectID, p.ID, pending.Revision)
	if saved.Items[0].Detail != "Unreviewed expansion" {
		t.Fatal("proposal lost")
	}
}

func TestTrackPolicyCannotBeEscapedByMovingNode(t *testing.T) {
	s, p := approvedFixture(t)
	s.Policy = Policy{Tracks: map[string]Rule{"elsewhere": {Mode: "agent"}}}
	next := modelNext(p)
	next.RoadmapNodeID = "elsewhere"
	v, err := s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "proposed" {
		t.Fatal(v, err)
	}
}

func TestDelegationCanRetireAndRestoreIntent(t *testing.T) {
	s, p := approvedFixture(t)
	s.Policy = Policy{Mode: "bounded", Allow: []string{"remove_items", "add_items"}}
	next := modelNext(p)
	next.Status = "superseded"
	v, err := s.AppendPlan(t.Context(), next)
	if err != nil || v.Change.Kind != "delegated" {
		t.Fatal(v, err)
	}
	next = modelNext(v)
	v, err = s.AppendPlan(t.Context(), next)
	if err != nil || v.Status != "active" || !oneOf("add_items", v.Change.Categories...) {
		t.Fatal(v, err)
	}
}
func TestFullAgencyCanCreatePlanAndPolicyCannotBeForgedInInput(t *testing.T) {
	ctx, s, p, _ := testService(t)
	s.Policy = Policy{Mode: "agent"}
	v, err := s.AppendPlan(ctx, Plan{ProjectID: p, ID: "agent-plan", RoadmapNodeID: "track", Title: "Agent intent", Summary: "Delegated", Status: "proposed", Creator: "agent", Model: "model", Change: &Change{Kind: "review"}, Items: []Item{{ID: "one", Title: "One", Status: "pending"}}})
	if err != nil || v.Status != "active" || v.Change.Kind != "delegated" {
		t.Fatal(v, err)
	}
	if err := (Policy{Mode: "bounded", Allow: []string{"typo"}}).Validate(); err == nil {
		t.Fatal("invalid delegation accepted")
	}
	pcy := MergePolicy(Policy{Mode: "review", Tracks: map[string]Rule{"track": {Mode: "bounded", Allow: []string{"add_items"}}}}, Policy{Tracks: map[string]Rule{"track": {Progress: "review"}}})
	if got := pcy.For("track"); got.Mode != "bounded" || got.Progress != "review" || len(got.Allow) != 1 {
		t.Fatal(got)
	}
}
