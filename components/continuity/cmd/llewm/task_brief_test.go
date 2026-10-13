package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/brief"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPTaskBriefRoutesExactlyWithoutDuplicateMarkdown(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "brief-task", "--task", "Exact task", "--summary", "Known state", "--next", "Continue exact work")
	cli := runCLI(t, "", "brief", "task:brief-task", "--output", "json")
	var b brief.TaskBrief
	if err := json.Unmarshal([]byte(cli), &b); err != nil || b.TaskID != "brief-task" {
		t.Fatalf("CLI: %s %v", cli, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	client := mcp.NewClient(&mcp.Implementation{Name: "task-brief-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_brief", Arguments: map[string]any{"ref": "task:brief-task"}})
	if err != nil || result.IsError {
		t.Fatalf("MCP: %+v %v", result, err)
	}
	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"task_id":"brief-task"`) || strings.Contains(string(body), `"markdown"`) {
		t.Fatalf("MCP duplicated or misrouted: %s", body)
	}
	missing, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_brief", Arguments: map[string]any{"ref": "task:missing"}})
	if err != nil || !missing.IsError {
		t.Fatalf("missing task: %+v %v", missing, err)
	}
}
