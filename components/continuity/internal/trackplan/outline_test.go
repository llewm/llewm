package trackplan

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutlineOmitsItemDetail(t *testing.T) {
	plan := Plan{ID: "p", Revision: 3, Title: "Plan", Status: "active", Items: []Item{
		{ID: "a", Position: 1, Title: "A", Status: "completed", Detail: "long detail", EvidenceRefs: []string{"commit:abc"}, WorkItemIDs: []string{"w"}},
		{ID: "b", Position: 2, Title: "B", Status: "pending", DependsOn: []string{"a"}},
	}}
	body, err := json.Marshal(plan.Outline())
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"long detail", "commit:abc", "work_item_ids", "evidence_refs"} {
		if strings.Contains(string(body), absent) {
			t.Fatalf("outline contains %q: %s", absent, body)
		}
	}
	if !strings.Contains(string(body), `"depends_on":["a"]`) || !strings.Contains(string(body), `"revision":3`) {
		t.Fatalf("outline lost structure: %s", body)
	}
	plan.Summary = strings.Repeat("é", OutlineSummaryBytes)
	if outline := plan.Outline(); !outline.SummaryTruncated || len(outline.Summary) > OutlineSummaryBytes+len("…") || !utf8.ValidString(outline.Summary) {
		t.Fatalf("summary not bounded: truncated=%v len=%d", outline.SummaryTruncated, len(outline.Summary))
	}
	if item, ok := plan.Item("a"); !ok || item.Detail != "long detail" {
		t.Fatalf("Item(a) = %+v, %v", item, ok)
	}
	if summary := plan.RevisionSummary(); summary.ItemCount != 2 || summary.Revision != 3 {
		t.Fatalf("revision summary = %+v", summary)
	}
}
