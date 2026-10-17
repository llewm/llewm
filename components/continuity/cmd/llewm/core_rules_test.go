package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
