package history

import (
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestSourceCatalogAndBoundedClaimToSourceNavigation(t *testing.T) {
	f := newFixture(t)
	s := worknarrative.Service{DB: f.db}
	obs := worknarrative.Record{ID: "web-source", ProjectID: f.projectID, Title: "Reference", Trigger: "manual", Creator: "agent", Source: &worknarrative.Source{URL: "https://example.org/reference?v=1", AccessedAt: "2026-10-18T00:00:00Z", Publisher: "Example Publisher", Authority: "caller_asserted"}, Claims: []worknarrative.Claim{{Kind: "note", Text: "Source summary", Authority: "caller_asserted"}}}
	if _, err := s.Append(f.ctx, obs); err != nil {
		t.Fatal(err)
	}
	finding := worknarrative.Record{ID: "web-finding", ProjectID: f.projectID, Title: "Finding", Trigger: "manual", Creator: "agent", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Scoped conclusion", Authority: "caller_asserted", Citations: []worknarrative.Citation{{Source: "narrative:web-source", Relation: "supports", Locator: "Section 2"}}}}}
	if _, err := s.Append(f.ctx, finding); err != nil {
		t.Fatal(err)
	}
	items := workitem.Service{DB: f.db}
	q, err := items.Capture(f.ctx, workitem.Capture{Item: workitem.Item{ProjectID: f.projectID, Kind: "research", Title: "What does the reference support?", Body: "Check scope", Creator: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Dispose(f.ctx, workitem.Disposition{ProjectID: f.projectID, ItemID: q.ID, Status: "answered", Reason: "Reviewed", Creator: "test", TargetRefs: []string{"narrative:web-finding#0"}}); err != nil {
		t.Fatal(err)
	}
	question, err := f.history.Show(f.ctx, f.projectID, []string{"work_item:" + q.ID}, 1024)
	if err != nil || !strings.Contains(question.Items[0].Text, "narrative:web-finding#0") {
		t.Fatalf("question findings lost: %+v %v", question, err)
	}
	page, err := f.history.Search(f.ctx, f.projectID, SearchOptions{SourceURL: obs.Source.URL})
	if err != nil || page.Returned != 2 || page.BodiesLoaded != 0 {
		t.Fatalf("catalog: %+v %v", page, err)
	}
	linked, err := f.history.Search(f.ctx, f.projectID, SearchOptions{Linked: "narrative:web-source"})
	if err != nil || strings.Join(refs(linked), ",") != "narrative:web-finding#0" {
		t.Fatalf("backlinks: %+v %v", linked, err)
	}
	obs.ID = "web-source-new"
	obs.SupersedesID = "web-source"
	obs.Source.Version = "2"
	if _, err := s.Append(f.ctx, obs); err != nil {
		t.Fatal(err)
	}
	shown, err := f.history.Show(f.ctx, f.projectID, []string{"narrative:web-finding#0", "narrative:web-source"}, 4096)
	if err != nil || !strings.Contains(shown.Items[0].Text, "source state: superseded") || !strings.Contains(shown.Items[0].Text, "Section 2") || !strings.Contains(shown.Items[1].Text, obs.Source.URL) || !strings.Contains(shown.Items[1].Text, "Accessed:") {
		t.Fatalf("show: %+v %v", shown, err)
	}
	if _, err := f.history.Reindex(f.ctx); err != nil {
		t.Fatal(err)
	}
	linked, err = f.history.Search(f.ctx, f.projectID, SearchOptions{Linked: "narrative:web-source"})
	if err != nil || linked.Returned != 1 {
		t.Fatalf("reindex lost citations: %+v %v", linked, err)
	}
	small, err := f.history.Show(f.ctx, f.projectID, []string{"narrative:web-finding#0", "narrative:web-source"}, 256)
	if err != nil || !small.Truncated || small.ContentBytes > 256 {
		t.Fatalf("bounds: %+v %v", small, err)
	}
}
