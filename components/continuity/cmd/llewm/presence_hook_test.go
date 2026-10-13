package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/llewm/llewm/components/continuity/internal/presence"
)

func TestHooksRecordExactPresenceLifecycle(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "live-task", "--task", "Live work", "--next", "Continue", "--session", "live-session")

	hook := func(client, event string) {
		t.Helper()
		payload := `{"session_id":"live-session","cwd":` + quoted(root) + `,"hook_event_name":` + quoted(event) + `}`
		runCLI(t, payload, "hook", client)
	}
	read := func(history bool) presence.Snapshot {
		t.Helper()
		args := []string{"presence", "--session", "live-session", "--output", "json"}
		if history {
			args = append(args, "--history")
		}
		var snapshot presence.Snapshot
		if err := json.Unmarshal([]byte(runCLI(t, "", args...)), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}

	hook("claude-code", "SessionStart")
	active := read(false)
	if len(active.Report.Presence) != 1 || active.Report.Presence[0].Client != "claude-code" || active.Report.Presence[0].TaskID != "live-task" || active.Report.Presence[0].WorktreePath != root || active.Report.Presence[0].DerivedState != "active" {
		t.Fatalf("active presence: %+v", active.Report.Presence)
	}
	hook("claude-code", "Stop")
	if got := read(false).Report.Presence; len(got) != 1 || got[0].DerivedState != "yielded" {
		t.Fatalf("yielded presence: %+v", got)
	}
	hook("claude-code", "UserPromptSubmit")
	if got := read(false).Report.Presence; len(got) != 1 || got[0].DerivedState != "active" {
		t.Fatalf("resumed presence: %+v", got)
	}
	hook("claude-code", "SessionEnd")
	if got := read(false).Report.Presence; len(got) != 1 || got[0].DerivedState != "completed" {
		t.Fatalf("completed presence: %+v", got)
	}
	history := read(true).Report.Presence
	if len(history) < 5 || history[0].Sequence < 5 {
		t.Fatalf("append-only lifecycle history: %+v", history)
	}
}

func TestCoordinationLeaseDoesNotImplyLivePresence(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "leased-task", "--task", "Leased work", "--next", "Continue")
	runCLI(t, "", "lease", "acquire", "task:leased-task", "--holder", "codex", "--session", "gone")

	output := runCLI(t, "", "presence", "--live", "--output", "json")
	var snapshot presence.Snapshot
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil || len(snapshot.Report.Presence) != 0 {
		t.Fatalf("lease appeared as live presence: %s (err=%v)", output, err)
	}
	bootstrap := runCLI(t, "", "bootstrap")
	if !strings.Contains(bootstrap, "- Task ID: leased-task\n") || strings.Contains(bootstrap, "another live session present") {
		t.Fatalf("lease blocked an otherwise selectable task:\n%s", bootstrap)
	}
}

func TestMCPToolCallsRefreshSoleClientPresenceAndDisconnectYields(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "mcp-live", "--task", "MCP live work", "--next", "Continue", "--session", "mcp-live-session")
	runCLI(t, `{"session_id":"mcp-live-session","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	client := mcp.NewClient(&mcp.Implementation{Name: "presence-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_info", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if got := presenceForSession(t, "mcp-live-session", true); len(got) < 3 || got[0].DerivedState != "active" {
		t.Fatalf("MCP call did not refresh active presence: %+v", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := presenceForSession(t, "mcp-live-session", false)
		if len(got) == 1 && got[0].DerivedState == "yielded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP disconnect did not yield presence: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func presenceForSession(t *testing.T, session string, history bool) []presence.Event {
	t.Helper()
	args := []string{"presence", "--session", session, "--output", "json"}
	if history {
		args = append(args, "--history")
	}
	var snapshot presence.Snapshot
	if err := json.Unmarshal([]byte(runCLI(t, "", args...)), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot.Report.Presence
}

func quoted(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
