package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeasesThroughTheCLI(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	acquired := runCLI(t, "", "lease", "acquire", "task:build", "--holder", "claude-code", "--session", "a", "--ttl", "10m")
	if !strings.Contains(acquired, "Acquired task:build with token 1 for claude-code (session a) until ") || !strings.Contains(acquired, "; rely on it until ") {
		t.Fatalf("acquire: %s", acquired)
	}
	if err, output := runCLIError("lease", "acquire", "task:build", "--holder", "claude-code", "--session", "b"); err == nil || !strings.Contains(err.Error()+output, "task:build is leased by claude-code (session a) (token 1)") {
		t.Fatalf("conflicting acquire: %v %s", err, output)
	}
	if again := runCLI(t, "", "lease", "acquire", "task:build", "--holder", "claude-code", "--session", "a"); !strings.Contains(again, "Already held; renewed task:build with token 1") {
		t.Fatalf("re-acquire: %s", again)
	}
	var renewedResult struct {
		Lease struct {
			Token    int64  `json:"token"`
			Renewals int    `json:"renewals"`
			State    string `json:"state"`
		} `json:"lease"`
	}
	err := json.Unmarshal([]byte(runCLI(t, "", "lease", "renew", "task:build", "--holder", "claude-code", "--session", "a", "--token", "1", "--output", "json")), &renewedResult)
	if renewed := renewedResult.Lease; err != nil || renewed.Token != 1 || renewed.Renewals != 2 || renewed.State != "held" {
		t.Fatalf("renew --json: %+v %v", renewedResult, err)
	}
	if err, output := runCLIError("lease", "renew", "task:build", "--holder", "claude-code", "--session", "a"); err == nil || !strings.Contains(err.Error()+output, "token of the held grant is required") {
		t.Fatalf("renew without a token: %v %s", err, output)
	}
	if err, output := runCLIError("lease", "acquire", "file:main.go", "--holder", "codex"); err == nil || !strings.Contains(err.Error()+output, "must be task:ID, roadmap_node:ID, or track_plan:ID") {
		t.Fatalf("file scope: %v %s", err, output)
	}
	runCLI(t, "", "lease", "acquire", "roadmap_node:sync", "--holder", "codex")
	list := runCLI(t, "", "lease", "list")
	if !strings.Contains(list, "roadmap_node:sync\ttoken 1\theld\tcodex\texpires ") || !strings.Contains(list, "task:build\ttoken 1\theld\tclaude-code (session a)\texpires ") || !strings.Contains(list, "Leases: 2. Leases are advisory unless") {
		t.Fatalf("lease list:\n%s", list)
	}

	runCLI(t, "", "checkpoint", "create", "--task", "Build", "--task-id", "build", "--next", "Continue")
	hook := runCLI(t, `{"session_id":"a","hook_event_name":"SessionStart","source":"resume"}`, "hook", "claude-code")
	if !strings.Contains(hook, "**Lease (machine-observed):** task:build is leased by claude-code (session a) (token 1, expires ") || !strings.Contains(hook, "1 other held lease (llewm lease list)") {
		t.Fatalf("bootstrap without a lease notice:\n%s", hook)
	}

	if released := runCLI(t, "", "lease", "release", "task:build", "--holder", "claude-code", "--session", "a", "--token", "1"); !strings.Contains(released, "Released task:build (token 1).") {
		t.Fatalf("release: %s", released)
	}
	if next := runCLI(t, "", "lease", "acquire", "task:build", "--holder", "claude-code", "--session", "b"); !strings.Contains(next, "with token 2 for claude-code (session b)") {
		t.Fatalf("acquire after release: %s", next)
	}
	show := runCLI(t, "", "lease", "show", "task:build")
	if !strings.Contains(show, "task:build\ttoken 2\theld\tclaude-code (session b)") || !strings.Contains(show, "task:build\ttoken 1\treleased\tclaude-code (session a)\treleased ") || !strings.Contains(show, "Grants: 2.") {
		t.Fatalf("lease show:\n%s", show)
	}
}

func TestRequiredLeasesFenceCheckpointsAndPlansThroughTheCLI(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	configPath := filepath.Join(root, ".continuity", "config.toml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(body, []byte("\n[leases]\nrequired = [\"task:locked\", \"track_plan:*\"]\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	// Advisory scope: the write proceeds, and a stale claim is noted on stderr.
	runCLI(t, "", "checkpoint", "create", "--task", "Free", "--task-id", "free", "--next", "Continue")
	if err, output := runCLIError("checkpoint", "create", "--task", "Locked", "--task-id", "locked", "--next", "Continue"); err == nil || !strings.Contains(err.Error()+output, "lease required: task:locked requires a current lease") {
		t.Fatalf("required checkpoint without a lease: %v %s", err, output)
	}
	runCLI(t, "", "lease", "acquire", "task:locked", "--holder", "claude-code", "--session", "a")
	if err, output := runCLIError("checkpoint", "create", "--task", "Locked", "--task-id", "locked", "--next", "Continue", "--lease-holder", "claude-code", "--lease-session", "b", "--lease-token", "1"); err == nil || !strings.Contains(err.Error()+output, "belongs to claude-code (session a)") {
		t.Fatalf("required checkpoint under another session's token: %v %s", err, output)
	}
	runCLI(t, "", "checkpoint", "create", "--task", "Locked", "--task-id", "locked", "--next", "Continue", "--lease-holder", "claude-code", "--lease-session", "a", "--lease-token", "1")

	plan := filepath.Join(t.TempDir(), "plan.json")
	definition := `{"id":"p","roadmap_node_id":"n","title":"Plan","summary":"Summary","status":"proposed","creator":"owner","items":[{"id":"a","position":1,"title":"Item","detail":"Detail","status":"pending"}]}`
	if err := os.WriteFile(plan, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	if err, output := runCLIError("track-plan", "record", "--file", plan); err == nil || !strings.Contains(err.Error()+output, "track_plan:p requires a current lease") {
		t.Fatalf("required plan without a lease: %v %s", err, output)
	}
	runCLI(t, "", "lease", "acquire", "track_plan:p", "--holder", "claude-code")
	recorded := runCLI(t, "", "track-plan", "record", "--file", plan, "--lease-holder", "claude-code", "--lease-token", "1")
	if !strings.Contains(recorded, `"state": "leased"`) || !strings.Contains(recorded, `"token": 1`) {
		t.Fatalf("plan under lease:\n%s", recorded)
	}
}
