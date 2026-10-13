package checkpoint

import (
	"context"
	"testing"
)

func TestListScopesAndPagesCheckpointLineage(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	var previous string
	for i := 0; i < 4; i++ {
		c, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "chain", Task: "Checkpoint", Summary: "handoff", Status: "in_progress", NextAction: "Continue", PredecessorCheckpointID: previous})
		if err != nil {
			t.Fatal(err)
		}
		previous = c.ID
	}
	if _, err := s.Create(ctx, Checkpoint{ProjectID: "p", TaskID: "other", Task: "Other", Summary: "other", NextAction: "Wait"}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.List(ctx, "p", ListRequest{TaskID: "chain", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.SchemaVersion != 1 || len(page.Checkpoints) != 2 {
			t.Fatalf("page: %+v", page)
		}
		for _, c := range page.Checkpoints {
			if c.TaskID != "chain" || seen[c.ID] {
				t.Fatalf("unscoped/duplicate row: %+v", c)
			}
			seen[c.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("seen %d checkpoints", len(seen))
	}
	empty, err := s.List(ctx, "missing-project", ListRequest{Limit: 2})
	if err != nil || len(empty.Checkpoints) != 0 {
		t.Fatalf("cross-project read: %+v, %v", empty, err)
	}
	for _, r := range []ListRequest{{Limit: -1}, {Limit: MaxHistoryLimit + 1}, {Cursor: "bad"}} {
		if _, err := s.List(ctx, "p", r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
