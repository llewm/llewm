package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=Continuity Test", "-c", "user.email=test@example.invalid"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// useProject switches the test to another machine's checkout at root, with its
// own Continuity store.
func useProject(t *testing.T, root string) {
	t.Helper()
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".llewm/continuity"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Chdir(root)
}

func TestMemoryAheadOfCodeIsReported(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	previous := startBackgroundSync
	startBackgroundSync = func(string, string) error { return nil }
	t.Cleanup(func() { startBackgroundSync = previous })
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, output)
	}

	// Machine A writes code, then a checkpoint on it, and syncs.
	rootA := testProject(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := gitIn(t, rootA, "rev-parse", "HEAD")
	// Machine B cloned the code before A's next commit.
	rootB := filepath.Join(t.TempDir(), "machine-b")
	gitIn(t, filepath.Dir(rootB), "clone", "--quiet", rootA, rootB)
	runCLI(t, "", "init")
	var identity struct {
		LineageID string `json:"lineage_id"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "init")), &identity); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "sync", "init", "--remote", remote)
	if err := os.WriteFile(filepath.Join(rootA, "feature.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, rootA, "add", "feature.txt")
	gitIn(t, rootA, "commit", "--quiet", "-m", "Add feature")
	ahead := gitIn(t, rootA, "rev-parse", "HEAD")
	runCLI(t, "", "checkpoint", "create", "--task", "Build the feature", "--task-id", "feature", "--next", "Test it on machine B")
	if status := runCLI(t, "", "work"); strings.Contains(status, "Code:") {
		t.Fatalf("machine A warned about its own code:\n%s", status)
	}
	runCLI(t, "", "sync")

	// Machine B has the memory but not the commit.
	useProject(t, rootB)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init", "--lineage", identity.LineageID)
	runCLI(t, "", "sync", "init", "--remote", remote)
	// The startup sync is detached; simulate its completion before checking
	// how a later bootstrap reports the imported checkpoint.
	runCLI(t, "", "sync")
	hook := runCLI(t, `{"session_id":"session-b","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if !strings.Contains(hook, "Build the feature") || !strings.Contains(hook, "Memory is ahead of code") || !strings.Contains(hook, ahead[:12]) || !strings.Contains(hook, "Pull first") {
		t.Fatalf("session start did not warn that memory is ahead of code:\n%s", hook)
	}
	if status := runCLI(t, "", "work"); !strings.Contains(status, "Code: Memory is ahead of code") {
		t.Fatalf("status did not warn:\n%s", status)
	}

	// After pulling, nothing to report.
	gitIn(t, rootB, "pull", "--quiet", "--ff-only", "origin", gitIn(t, rootA, "branch", "--show-current"))
	if status := runCLI(t, "", "work"); strings.Contains(status, "Code:") {
		t.Fatalf("warned after pulling:\n%s", status)
	}

	// On a branch without the commit, the code has moved off the checkpoint.
	gitIn(t, rootB, "checkout", "--quiet", "-b", "side", base)
	if status := runCLI(t, "", "work"); !strings.Contains(status, "Code is behind or off the checkpoint") {
		t.Fatalf("status on a side branch:\n%s", status)
	}

	// Squashing the checkpoint's branch into this one brings its changes
	// without its commit.
	gitIn(t, rootB, "merge", "--quiet", "--squash", ahead)
	gitIn(t, rootB, "commit", "--quiet", "-m", "Squash the feature")
	if status := runCLI(t, "", "work"); !strings.Contains(status, "landed here by squash or rebase merge") || strings.Contains(status, "Code is behind") {
		t.Fatalf("status after a squash merge:\n%s", status)
	}
}
