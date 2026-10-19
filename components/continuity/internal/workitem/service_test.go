package workitem

import (
	"context"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
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
	a, err := store.EnsureProject(ctx, "/first", "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.EnsureProject(ctx, "/second", "second")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Now: func() time.Time { return now }}, a, b
}

func capture(t *testing.T, ctx context.Context, s Service, v Item) Item {
	t.Helper()
	created, err := s.Capture(ctx, Capture{Item: v})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestWorkflowIsolationRedactionAndAudit(t *testing.T) {
	ctx, s, first, second := testService(t)
	v := capture(t, ctx, s, Item{ID: "one", ProjectID: first, Kind: "idea", Title: "Cache friction", Body: "password=idea-secret", Tags: []string{"sandbox", "go"}, Creator: "human"})
	if strings.Contains(v.Body, "idea-secret") || !strings.Contains(v.Body, "[REDACTED]") || v.Status != "pending" || strings.Join(v.Tags, ",") != "go,sandbox" {
		t.Fatalf("item not normalized: %#v", v)
	}
	if other, err := s.Show(ctx, second, "work_item:one", 512); err != nil || other != nil {
		t.Fatalf("cross-project item leaked: %#v %v", other, err)
	}
	page, err := s.ListPage(ctx, first, ListOptions{Status: "pending", Tag: "go"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "one" || page.Items[0].Kind != "idea" {
		t.Fatalf("unexpected list: %#v %v", page, err)
	}
	audit, err := s.Audit(ctx, first, "", 512)
	if err != nil || audit.PendingCount != 1 || audit.IncludedCount != 1 || audit.Truncated || !strings.Contains(audit.Markdown, "work_item:one") {
		t.Fatalf("unexpected audit: %#v %v", audit, err)
	}
	if _, err = s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "one", Status: "accepted", Reason: "No target", Creator: "human"}); err == nil {
		t.Fatal("accepted without a target succeeded")
	}
	d, err := s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "work_item:one", Status: "accepted", Reason: "Adopt this direction", TargetRefs: []string{"plan:cache/item"}, Creator: "human"})
	if err != nil || d.Status != "accepted" {
		t.Fatalf("unexpected disposition: %#v %v", d, err)
	}
	if _, err = s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "one", Status: "accepted", Reason: "Again", TargetRefs: []string{"plan:x"}, Creator: "human"}); err == nil {
		t.Fatal("second acceptance succeeded")
	}
	open, err := s.ListPage(ctx, first, ListOptions{Status: "open"})
	if err != nil || len(open.Items) != 1 || open.Items[0].Status != "accepted" {
		t.Fatalf("accepted item not open: %#v %v", open, err)
	}
	if _, err = s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "one", Status: "integrated", Reason: "Implemented", TargetRefs: []string{"commit:abc"}, Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "one", Status: "discarded", Reason: "Again", Creator: "human"}); err == nil {
		t.Fatal("transition after a terminal disposition succeeded")
	}
	shown, err := s.Show(ctx, first, "one", 2048)
	if err != nil || shown.Item.Status != "integrated" || !strings.Contains(shown.Markdown, "Disposition history") || !strings.Contains(shown.Markdown, "plan:cache/item") || !strings.Contains(shown.Markdown, "commit:abc") {
		t.Fatalf("disposition history missing from show: %#v %v", shown, err)
	}
	if _, err = s.Dispose(ctx, Disposition{ProjectID: first, ItemID: "missing", Status: "discarded", Reason: "No item", Creator: "human"}); err == nil {
		t.Fatal("disposition of a missing item succeeded")
	}
}

func TestDeferredItemsStayOpen(t *testing.T) {
	ctx, s, project, _ := testService(t)
	capture(t, ctx, s, Item{ID: "later", ProjectID: project, Kind: "task", Title: "Later", Body: "Not now", Creator: "human"})
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: "later", Status: "deferred", Reason: "After the release", Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: "later", Status: "discarded", Reason: "No longer needed", Creator: "human"}); err != nil {
		t.Fatalf("deferred item could not be closed: %v", err)
	}
}

func TestBugFieldsAndValidation(t *testing.T) {
	ctx, s, project, _ := testService(t)
	bug := capture(t, ctx, s, Item{ID: "crash", ProjectID: project, Kind: "bug", Title: "Sync crashes", Body: "Sync exits early", Observed: "panic in import", Reproduction: "run llewm sync twice", Environment: "linux, go 1.27", Severity: "high", Creator: "human"})
	shown, err := s.Show(ctx, project, bug.ID, 4096)
	if err != nil || shown.Item.Severity != "high" || !strings.Contains(shown.Markdown, "## Reproduction") || !strings.Contains(shown.Markdown, "panic in import") {
		t.Fatalf("bug fields missing: %#v %v", shown, err)
	}
	bad := []Item{
		{ProjectID: project, Kind: "idea", Title: "t", Body: "b", Severity: "high", Creator: "human"},
		{ProjectID: project, Kind: "bug", Title: "t", Body: "b", Severity: "urgent", Creator: "human"},
		{ProjectID: project, Kind: "epic", Title: "t", Body: "b", Creator: "human"},
		{ProjectID: project, Kind: "task", Body: "b", Creator: "human"},
		{ProjectID: project, Kind: "task", Title: "t", Body: "b"},
	}
	for i, v := range bad {
		if _, err := s.Capture(ctx, Capture{Item: v}); err == nil {
			t.Fatalf("invalid item %d succeeded", i)
		}
	}
}

func TestPromotionAndFrictionTracking(t *testing.T) {
	ctx, s, project, _ := testService(t)
	capture(t, ctx, s, Item{ID: "seed", ProjectID: project, Kind: "idea", Title: "Seed", Body: "Could be a task", Creator: "human"})
	task, err := s.Capture(ctx, Capture{Item: Item{ID: "do-it", ProjectID: project, Kind: "task", Title: "Do it", Body: "Concrete work", Creator: "human"}, From: "work_item:seed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Links) != 1 || task.Links[0].Relationship != "derived_from" || task.Links[0].TargetRef != "work_item:seed" {
		t.Fatalf("promotion link missing: %#v", task.Links)
	}
	seed, err := s.Get(ctx, project, "seed")
	if err != nil || seed.Status != "integrated" || seed.Dispositions[0].TargetRefs[0] != "work_item:do-it" {
		t.Fatalf("promoted item not integrated: %#v %v", seed, err)
	}
	if _, err := s.Capture(ctx, Capture{Item: Item{ProjectID: project, Kind: "task", Title: "Again", Body: "b", Creator: "human"}, From: "work_item:seed"}); err == nil {
		t.Fatal("promoting a closed item succeeded")
	}
	if _, err := s.Capture(ctx, Capture{Item: Item{ProjectID: project, Kind: "bug", Title: "Friction", Body: "b", Creator: "human"}, From: "friction:00112233445566778899aabbccddeeff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture(ctx, Capture{Item: Item{ProjectID: project, Kind: "bug", Title: "Bad", Body: "b", Creator: "human"}, From: "plan:x"}); err == nil {
		t.Fatal("unsupported from reference succeeded")
	}
	counts, err := s.Counts(ctx, project)
	if err != nil || counts.Open != 2 || counts.ByKind["task"] != 1 || counts.ByKind["bug"] != 1 {
		t.Fatalf("unexpected counts: %#v %v", counts, err)
	}
	if notice := Notice(counts); notice != "2 open (1 bug, 1 task); list with llewm item list --status open." {
		t.Fatalf("unexpected notice: %q", notice)
	}
}

func TestListAndRenderBounds(t *testing.T) {
	ctx, s, project, _ := testService(t)
	for i := 0; i < 3; i++ {
		capture(t, ctx, s, Item{ID: fmt.Sprintf("item-%d", i), ProjectID: project, Kind: "idea", Title: "Large", Body: strings.Repeat("x", 600), Tags: []string{"bounds"}, Creator: "human"})
	}
	audit, err := s.Audit(ctx, project, "idea", 512)
	if err != nil || !audit.Truncated || audit.IncludedCount >= audit.PendingCount || audit.RenderedBytes > 512 {
		t.Fatalf("unexpected bounded audit: %#v %v", audit, err)
	}
	shown, err := s.Show(ctx, project, "item-0", 512)
	if err != nil || !shown.Truncated || shown.RenderedBytes > 512 || shown.EstimatedTokens != (shown.RenderedBytes+3)/4 {
		t.Fatalf("unexpected bounded show: %#v %v", shown, err)
	}
	page, err := s.ListPage(ctx, project, ListOptions{Kind: "idea", Tag: "bounds", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Total != 3 || !page.Truncated || page.Items[0].ID != "item-0" {
		t.Fatalf("unexpected page: %#v %v", page, err)
	}
	empty, err := s.ListPage(ctx, project, ListOptions{Kind: "bug"})
	if err != nil || len(empty.Items) != 0 || empty.Total != 0 || empty.Truncated {
		t.Fatalf("unexpected empty page: %#v %v", empty, err)
	}
	if _, err := s.ListPage(ctx, project, ListOptions{Status: "rejected"}); err == nil {
		t.Fatal("unsupported status succeeded")
	}
}

func TestResearchItemsCloseAsAnsweredWithFindings(t *testing.T) {
	ctx, s, project, _ := testService(t)
	if _, err := (worknarrative.Service{DB: s.DB}).Append(ctx, worknarrative.Record{ID: "n", ProjectID: project, Title: "Findings", Creator: "test", Trigger: "manual", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Result", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	question := capture(t, ctx, s, Item{ProjectID: project, Kind: "research", Title: "Which cache fails?", Body: "Find out", Creator: "human"})
	idea := capture(t, ctx, s, Item{ProjectID: project, Kind: "idea", Title: "Idea", Body: "Body", Creator: "human"})
	counts, err := s.Counts(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	if notice := Notice(counts); !strings.Contains(notice, "1 idea, 1 open question") {
		t.Fatalf("notice %q", notice)
	}
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: question.ID, Status: "answered", Reason: "Found", Creator: "human"}); err == nil {
		t.Fatal("answered without findings")
	}
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: idea.ID, Status: "answered", Reason: "Found", TargetRefs: []string{"narrative:n"}, Creator: "human"}); err == nil || !strings.Contains(err.Error(), "only a research item") {
		t.Fatalf("answered an idea: %v", err)
	}
	for _, ref := range []string{"narrative:missing", "narrative:n#55", "https://example.org", "decision:missing"} {
		if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: question.ID, Status: "answered", Reason: "Found", TargetRefs: []string{ref}, Creator: "human"}); err == nil {
			t.Fatalf("accepted invalid answer %s", ref)
		}
	}
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: question.ID, Status: "answered", Reason: "Found", TargetRefs: []string{"narrative:n"}, Creator: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, Disposition{ProjectID: project, ItemID: question.ID, Status: "deferred", Reason: "Again", Creator: "human"}); err == nil {
		t.Fatal("changed an answered item")
	}
	page, err := s.ListPage(ctx, project, ListOptions{Kind: "research", Status: "answered", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != question.ID {
		t.Fatalf("answered list %#v err=%v", page, err)
	}
}
