package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionCLIRecordAndReadOnlyViews(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	path := filepath.Join(root, "decision.json")
	body := `{"id":"choice-a","title":"Choose local state","rationale":"Keep token=private-value local","disposition":"accepted","actor":"owner","actor_type":"human","authority":"user_reported","review_source":"checkpoint:review-1","links":[{"kind":"subject","target_kind":"roadmap_node","target_id":"hygiene","target_revision":"5"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	created := runCLI(t, "", "decision", "record", "--file", path)
	if !strings.Contains(created, `"id": "choice-a"`) || strings.Contains(created, "private-value") || !strings.Contains(created, "[REDACTED]") {
		t.Fatalf("record: %s", created)
	}
	if replay := runCLI(t, "", "decision", "record", "--file", path); !strings.Contains(replay, `"replay": true`) {
		t.Fatalf("replay: %s", replay)
	}
	linked := runCLI(t, "", "history", "search", "--linked", "roadmap_node:hygiene")
	if !strings.Contains(linked, "decision:choice-a") || !strings.Contains(linked, "bodies_loaded=0") {
		t.Fatalf("linked search: %s", linked)
	}
	show := runCLI(t, "", "history", "show", "decision:choice-a")
	for _, want := range []string{"== decision:choice-a", "Keep token=[REDACTED] local", "subject roadmap_node:hygiene@5", "review checkpoint:review-1"} {
		if !strings.Contains(show, want) {
			t.Fatalf("show missing %q: %s", want, show)
		}
	}
	var payload any
	if err := json.Unmarshal([]byte(runCLI(t, "", "history", "show", "decision:choice-a", "--output", "json")), &payload); err != nil {
		t.Fatal(err)
	}
}
