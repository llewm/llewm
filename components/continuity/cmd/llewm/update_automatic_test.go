package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
)

func TestSessionStartAutomaticUpdateFailureIsIsolatedAndConfigurable(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "update-test", "--task", "Automatic update test", "--next", "Continue")
	hook := func(client string) string {
		return runCLI(t, `{"session_id":"update-session","hook_event_name":"SessionStart"}`, "hook", client)
	}
	if out := hook("codex"); strings.Contains(out, "Llewm update check:") {
		t.Fatalf("default enabled: %s", out)
	}
	runCLI(t, "", "update", "configure", "--enabled", "--source-root", root, "--interval", "1h")
	for _, client := range []string{"codex", "claude-code"} {
		out := hook(client)
		if !strings.Contains(out, "Automatic update test") || !strings.Contains(out, "Llewm update check: unavailable") {
			t.Fatalf("hook failed or notice missing: %s", out)
		}
	}
	cache := filepath.Join(os.Getenv("CONTINUITY_GLOBAL_HOME"), "update-check-cache.json")
	before, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "update", "status")
	after, err := os.ReadFile(cache)
	if err != nil || string(before) != string(after) {
		t.Fatal("status performed a check")
	}
	runCLI(t, "", "update", "configure", "--enabled=false")
	var cfg updatecheck.AutomaticConfig
	if err := json.Unmarshal([]byte(runCLI(t, "", "update", "status")), &cfg); err != nil || cfg.Enabled || cfg.SourceRoot != root || cfg.Interval != "1h" {
		t.Fatalf("disable discarded settings: %+v %v", cfg, err)
	}
	if out := hook("codex"); strings.Contains(out, "Llewm update check:") {
		t.Fatalf("disabled check: %s", out)
	}
}
