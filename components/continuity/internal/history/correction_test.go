package history

import (
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestCorrectedClaimsRemainHistoricalAndExposeSuccessor(t *testing.T) {
	f := newFixture(t)
	_, err := (worknarrative.Service{DB: f.db}).Append(f.ctx, worknarrative.Record{
		ID: "correction", ProjectID: f.projectID, Title: "Correct reminders", Trigger: "manual", Creator: "test", SupersedesID: "plan-cards",
		Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Updated reminders", Authority: "caller_asserted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Families: []string{"narrative_claim"}, Query: "reminders"})
	if err != nil || strings.Join(refs(current), ",") != "narrative:correction#0" {
		t.Fatalf("current: %+v %v", current, err)
	}
	old, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Families: []string{"narrative_claim"}, Query: "reminders", State: "superseded"})
	if err != nil || len(old.Hits) != 1 || old.Hits[0].State != "superseded" || strings.Join(old.Hits[0].Successors, ",") != "narrative:correction" {
		t.Fatalf("history: %+v %v", old, err)
	}
	all, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Families: []string{"narrative_claim"}, Query: "reminders", State: "all"})
	if err != nil || len(all.Hits) != 2 {
		t.Fatalf("all: %+v %v", all, err)
	}
	page, err := f.history.Show(f.ctx, f.projectID, []string{"narrative:plan-cards#0"}, 256)
	if err != nil || !strings.HasPrefix(page.Items[0].Text, "State: superseded\nSuccessors: narrative:correction") || page.ContentBytes > 256 {
		t.Fatalf("show: %+v %v", page, err)
	}
	missing, err := f.history.Search(f.ctx, "other-project", SearchOptions{Families: []string{"narrative_claim"}, Query: "reminders", State: "all"})
	if err != nil || len(missing.Hits) != 0 {
		t.Fatalf("project isolation: %+v %v", missing, err)
	}
}
