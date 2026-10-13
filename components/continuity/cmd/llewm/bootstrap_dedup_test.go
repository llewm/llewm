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
)

func TestMCPBootstrapSkipsThePacketTheSessionStartHookDelivered(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task", "Dedup task", "--task-id", "dedup", "--next", "Continue")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "dedup", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	bootstrap := func(args map[string]any) (string, bool, int) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_bootstrap", Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("bootstrap %v: %v %#v", args, err, result)
		}
		body, _ := json.Marshal(result.StructuredContent)
		var output struct {
			Markdown  string `json:"markdown"`
			Unchanged bool   `json:"unchanged"`
		}
		if err := json.Unmarshal(body, &output); err != nil {
			t.Fatal(err)
		}
		return output.Markdown, output.Unchanged, len(body)
	}

	// No hook delivery yet: the whole packet.
	if markdown, unchanged, _ := bootstrap(map[string]any{}); unchanged || !strings.Contains(markdown, "Dedup task") {
		t.Fatalf("bootstrap without a delivery: unchanged=%v\n%s", unchanged, markdown)
	}
	runCLI(t, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	markdown, unchanged, size := bootstrap(map[string]any{})
	if !unchanged || size > 1024 || !strings.Contains(markdown, "Unchanged since this session's SessionStart bootstrap") {
		t.Fatalf("repeat bootstrap: unchanged=%v size=%d\n%s", unchanged, size, markdown)
	}
	if markdown, unchanged, _ := bootstrap(map[string]any{"full": true}); unchanged || !strings.Contains(markdown, "Dedup task") {
		t.Fatalf("full bootstrap: unchanged=%v\n%s", unchanged, markdown)
	}
	if markdown, unchanged, _ := bootstrap(map[string]any{"task_id": "dedup"}); unchanged || !strings.Contains(markdown, "Dedup task") {
		t.Fatalf("selected bootstrap: unchanged=%v\n%s", unchanged, markdown)
	}

	// A new checkpoint changes the packet.
	runCLI(t, "", "checkpoint", "create", "--task", "Dedup task", "--task-id", "dedup", "--predecessor-checkpoint", taskTip(t, "dedup"), "--next", "Changed next step")
	if markdown, unchanged, _ := bootstrap(map[string]any{}); unchanged || !strings.Contains(markdown, "Changed next step") {
		t.Fatalf("changed bootstrap: unchanged=%v\n%s", unchanged, markdown)
	}

	// Deliveries to two sessions cannot say which is asking, unless it names itself.
	runCLI(t, `{"session_id":"s1","hook_event_name":"SessionStart","source":"compact"}`, "hook", "claude-code")
	runCLI(t, `{"session_id":"s2","hook_event_name":"SessionStart","source":"startup"}`, "hook", "claude-code")
	if _, unchanged, _ := bootstrap(map[string]any{}); unchanged {
		t.Fatal("bootstrap counted as unchanged across two sessions")
	}
	if _, unchanged, _ := bootstrap(map[string]any{"current_session_id": "s2"}); !unchanged {
		t.Fatal("bootstrap naming its session was not unchanged")
	}
}
