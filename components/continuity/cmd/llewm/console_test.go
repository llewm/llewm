package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestConsoleCanonicalReadsDoNotModifyStore(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	id := strings.TrimSpace(runCLI(t, "", "checkpoint", "create", "--task", "Console handoff", "--task-id", "console-task", "--summary", "Read-only fixture", "--next", "Inspect"))
	project := projectShow(t)
	before, err := os.ReadFile(project.Database)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"project", "checkpoints", "checkpoint", "health", "releases"} {
		args := []string{"console", "view", view}
		if view == "checkpoint" {
			args = append(args, "--id", id)
		}
		body := runCLI(t, "", args...)
		var envelope struct {
			SchemaVersion int             `json:"schema_version"`
			ProjectID     string          `json:"project_id"`
			View          string          `json:"view"`
			Data          json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.SchemaVersion != 1 || envelope.ProjectID != project.ProjectID || envelope.View != view {
			t.Fatalf("view %s: %s", view, body)
		}
		if view == "checkpoints" && !strings.Contains(string(envelope.Data), id) {
			t.Fatalf("checkpoint missing: %s", body)
		}
	}
	after, err := os.ReadFile(project.Database)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("console views modified the database")
	}
	for _, args := range [][]string{{"console", "view", "unknown"}, {"console", "view", "checkpoint"}, {"console", "view", "health", "--task-id", "other"}} {
		if err, _ := runCLIError(args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
