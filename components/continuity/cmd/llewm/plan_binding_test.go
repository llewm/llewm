package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordingAPlanBindsItsTaskToItsNode(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "roadmap", "node", "--id", "work-node", "--title", "Work", "--status", "active", "--source-path", "AGENTS.md")
	runCLI(t, "", "checkpoint", "create", "--task", "Work", "--task-id", "work-task", "--next", "Continue")
	if got := runCLI(t, "", "work", "inventory", "--node", "work-node"); !strings.Contains(got, `"binding_state": "missing"`) {
		t.Fatalf("before the plan:\n%s", got)
	}
	plan := `{"id":"work-plan","roadmap_node_id":"work-node","task_id":"work-task","title":"Work","summary":"Work","status":"proposed","creator":"human","items":[{"id":"one","position":1,"title":"One","status":"pending"}]}`
	path := filepath.Join(root, "plan.json")
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "track-plan", "record", "--file", path)
	runCLI(t, "", "track-plan", "record", "--file", path)
	got := runCLI(t, "", "work", "inventory", "--node", "work-node")
	if !strings.Contains(got, `"binding_state": "explicit"`) || !strings.Contains(got, `"selected_task_id": "work-task"`) || strings.Contains(got, "ambiguous") {
		t.Fatalf("after the plan:\n%s", got)
	}
}
