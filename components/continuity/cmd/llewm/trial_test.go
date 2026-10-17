package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
)

func TestTrialHandbackPreservesLearningAndConcurrentProse(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "trial", "start", "--prose", "plan.md")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "trial-recovery", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	beforeCall, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_info", Arguments: map[string]any{}})
	if err != nil || beforeCall.IsError {
		t.Fatalf("trial MCP unavailable: %v %+v", err, beforeCall)
	}

	// Startup must not direct work from stored task state, even after checkpointing.
	runCLI(t, "", "checkpoint", "create", "--task-id", "learned", "--task", "Trial learning", "--summary", "Keep the newly discovered recovery requirement", "--next", "Review new recovery knowledge")
	if out := runCLI(t, `{"session_id":"trial-session","hook_event_name":"SessionStart"}`, "hook", "codex"); out != "" {
		t.Fatalf("trial startup directed work: %s", out)
	}
	requireCLIError(t, "trial", "bootstrap")
	a, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Config.Sync.Remote != "" {
		t.Fatal("trial enabled sync")
	}
	a.Close()
	if err = os.WriteFile(filepath.Join(root, "plan.md"), []byte("A concurrent plan edit must survive.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "trial", "prepare", "--out", "review1")
	report, err := os.ReadFile(filepath.Join(root, "review1", "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"newly discovered recovery requirement", "concurrent plan edit", "Historical revisions", "Missing references"} {
		if !strings.Contains(string(report), text) {
			t.Fatalf("missing %s", text)
		}
	}
	// New knowledge invalidates the prepared review, rather than silently losing it.
	runCLI(t, "", "checkpoint", "create", "--task-id", "later", "--task", "Later learning", "--summary", "More trial knowledge", "--next", "Reconcile")
	handback := "# Continue here\n\nCurrent work: reconcile new recovery knowledge and the concurrent plan.\n"
	if err = os.WriteFile(filepath.Join(root, "HANDBACK.md"), []byte(handback), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"trial", "exit", "--review", "review1", "--handback", "HANDBACK.md", "--reviewed", "--writers-stopped", "--verification", "Fresh session with integrations disabled read HANDBACK.md and identified next work"}
	requireCLIError(t, "changed since review", args...)
	runCLI(t, "", "trial", "prepare", "--out", "review2")
	args[3] = "review2"
	// Keep an app open to demonstrate its write guard observes the disconnect.
	old, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	runCLI(t, "", args...)
	afterCall, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_info", Arguments: map[string]any{}})
	if callErr == nil && !afterCall.IsError {
		t.Fatal("already-open MCP served after exit")
	}

	if err = old.CheckWriter(context.Background()); err == nil {
		t.Fatal("old app remained writable")
	}
	if _, err = app.Open(context.Background(), root); err == nil {
		t.Fatal("exited project reopened")
	}
	if out := runCLI(t, `{"session_id":"after","hook_event_name":"SessionStart"}`, "hook", "codex"); out != "" {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "HANDBACK.md")); string(b) != handback {
		t.Fatal("handback modified")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "plan.md")); !strings.Contains(string(b), "concurrent plan edit") {
		t.Fatal("prose overwritten")
	}
	if _, err = os.Stat(config.Default(root).Storage.Database); err != nil {
		t.Fatal("database deleted")
	}
	if !strings.Contains(runCLI(t, "", "trial", "status"), `"phase": "exited"`) {
		t.Fatal("exit not persisted")
	}
}
func TestTrialExplicitAdoptionAndReviewSafety(t *testing.T) {
	root := testProject(t)
	if err := os.WriteFile(filepath.Join(root, "plan.md"), []byte("token=private-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "trial", "start", "--prose", "plan.md")
	b, _ := os.ReadFile(filepath.Join(config.StateDir(root), "adoption.json"))
	if strings.Contains(string(b), "private-test-token") {
		t.Fatal("unredacted baseline")
	}
	requireCLIError(t, "--reviewed", "trial", "adopt")
	if _, err := config.SetSync(root, config.Sync{Remote: "ssh://example.invalid/memory"}); err == nil {
		t.Fatal("trial configured sync")
	}
	runCLI(t, "", "trial", "adopt", "--reviewed")
	if !strings.Contains(runCLI(t, "", "bootstrap"), "Continuity") {
		t.Fatal("adopted bootstrap unavailable")
	}
	runCLI(t, "", "trial", "prepare", "--out", "review")
	requireCLIError(t, "must not exist", "trial", "prepare", "--out", "review")
	requireCLIError(t, "project-relative", "trial", "prepare", "--out", "../escape")
	// Changes to tracked prose invalidate review too.
	if err := os.WriteFile(filepath.Join(root, "plan.md"), []byte("New prose\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "HAND.md"), []byte("Reviewed handback\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireCLIError(t, "changed since review", "trial", "exit", "--review", "review", "--handback", "HAND.md", "--reviewed", "--writers-stopped", "--verification", "Fresh session read HAND.md without integrations")
}
func requireCLIError(t *testing.T, want string, args ...string) {
	t.Helper()
	cmd := newRootCommand("llewm")
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%v error=%v, want %s", args, err, want)
	}
}
