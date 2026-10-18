package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
)

func TestCoreRulesAreBuiltInAndConfigurable(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "work", "--task", "Work", "--next", "Continue")
	if got := runCLI(t, "", "bootstrap"); !strings.Contains(got, "## Continuity rules (built in)\n\n- discovery: Before diagnosing") {
		t.Fatalf("built-in rules missing:\n%s", got)
	}
	configPath := filepath.Join(root, ".llewm/continuity", "config.toml")
	base, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	write := func(extra string) {
		t.Helper()
		if err := os.WriteFile(configPath, append(append([]byte{}, base...), extra...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("\n[guidance.core]\n  disable = [\"own-commits\"]\n[guidance.core.override]\n  discovery = \"Search history first.\"\n")
	got := runCLI(t, "", "bootstrap")
	if !strings.Contains(got, "- discovery: Search history first. (overridden by project config)") || !strings.Contains(got, "- Disabled by configuration: own-commits.") || strings.Contains(got, "- own-commits:") {
		t.Fatalf("project configuration not applied:\n%s", got)
	}
	// A typo never fails the bootstrap: the built-in rules apply, with a warning.
	write("\n[guidance.core]\n  disable = [\"discvery\"]\n")
	got = runCLI(t, "", "bootstrap")
	if !strings.Contains(got, "- discovery: Before diagnosing") || !strings.Contains(got, `Configuration ignored: project config: unknown core rule "discvery"`) {
		t.Fatalf("invalid configuration not reported:\n%s", got)
	}
}

func TestCheckoutLifecycleReachesNewAndResumedTasksWithoutProjectEdits(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	guidancePath := filepath.Join(root, "AGENTS.md")
	original, err := os.ReadFile(guidancePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"new", "resumed", "ambiguous"} {
		if phase != "new" {
			runCLI(t, "", "checkpoint", "create", "--task-id", phase, "--task", phase+" task", "--next", "Continue")
		}
		var packet bootstrap.Packet
		if err := json.Unmarshal([]byte(runCLI(t, "", "bootstrap", "--output", "json")), &packet); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rule := range packet.CoreRules {
			if rule.ID == "checkout-lifecycle" && rule.Source == "built-in" && rule.Text != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: missing binary-owned checkout rule", phase)
		}
		if phase == "new" && packet.Checkpoint != nil || phase == "resumed" && packet.Checkpoint == nil || phase == "ambiguous" && (packet.Choice == nil || packet.Choice.Rule != bootstrap.RuleAmbiguous) {
			t.Fatalf("unexpected %s fixture state: %+v", phase, packet)
		}
		for _, args := range [][]string{{"bootstrap"}, {"hook", "codex", "SessionStart"}, {"hook", "claude-code", "SessionStart"}} {
			payload := `{"session_id":"` + phase + "-" + args[0] + "-" + args[len(args)-1] + `"}`
			if got := runCLI(t, payload, args...); !strings.Contains(got, "- checkout-lifecycle:") || !strings.Contains(got, "- resume-completion:") {
				t.Fatalf("%s %v: missing checkout rule: %s", phase, args, got)
			}
		}
	}
	current, err := os.ReadFile(guidancePath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("bootstrap rewrote user project guidance: %v", err)
	}
}

func TestCheckoutLifecycleCanBeOverriddenOrDisabled(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	path := filepath.Join(root, ".llewm/continuity", "config.toml")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, disable := range []bool{false, true} {
		extra := "\n[guidance.core.override]\ncheckout-lifecycle = \"Use the project's required review branch workflow.\"\n"
		if disable {
			extra = "\n[guidance.core]\ndisable = [\"checkout-lifecycle\"]\n"
		}
		if err := os.WriteFile(path, append(append([]byte{}, base...), extra...), 0600); err != nil {
			t.Fatal(err)
		}
		var packet bootstrap.Packet
		if err := json.Unmarshal([]byte(runCLI(t, "", "bootstrap", "--output", "json")), &packet); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rule := range packet.CoreRules {
			if rule.ID == "checkout-lifecycle" {
				found = true
				if disable || rule.Source != "overridden by project config" || rule.Text != "Use the project's required review branch workflow." {
					t.Fatalf("project rule not respected: %+v", rule)
				}
			}
		}
		if disable {
			if len(packet.CoreRulesDisabled) != 1 || packet.CoreRulesDisabled[0] != "checkout-lifecycle" {
				t.Fatalf("disabled rule not reported: %v", packet.CoreRulesDisabled)
			}
		} else if !found {
			t.Fatal("missing project override")
		}
	}
}

func TestMemoryRepositoryRuleAppearsOnlyWithSync(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "work", "--task", "Work", "--next", "Continue")
	if got := runCLI(t, "", "bootstrap"); strings.Contains(got, "memory-repository") {
		t.Fatalf("memory rule shown without sync:\n%s", got)
	}
	configPath := filepath.Join(root, ".llewm/continuity", "config.toml")
	base, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "memory-clone")
	if err := os.WriteFile(configPath, append(base, []byte("\n[sync]\n  remote = \"https://example.invalid/memory.git\"\n  clone = \""+clone+"\"\n  manual = true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCLI(t, "", "bootstrap"); !strings.Contains(got, "- memory-repository: The memory repository clone at `"+clone+"` is managed by Continuity sync.") {
		t.Fatalf("memory rule missing with sync:\n%s", got)
	}
}
