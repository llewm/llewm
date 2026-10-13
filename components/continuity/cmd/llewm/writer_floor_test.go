package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
)

func TestOnlyWriterIncompatibleProcessesStopWriting(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	if buildinfo.WriterAPIVersion > buildinfo.MCPAPIVersion {
		t.Fatalf("writer API exceeds MCP API: writer=%d MCP=%d", buildinfo.WriterAPIVersion, buildinfo.MCPAPIVersion)
	}
	if got := runCLI(t, "", "database", "writer-floor"); !strings.Contains(got, fmt.Sprintf("Writer floor: writer API %d", buildinfo.WriterAPIVersion)) {
		t.Fatalf("opening the store did not raise the floor to this build: %s", got)
	}
	// Ordinary MCP API revisions do not strand compatible writers.
	runCLI(t, "", "checkpoint", "create", "--task-id", "compatible", "--task", "Compatible", "--next", "Continue")
	// Simulate a write-incompatible build having opened the store.
	newer := buildinfo.WriterAPIVersion + 1
	runCLI(t, "", "database", "writer-floor", "--set", fmt.Sprint(newer))
	err, output := runCLIError("checkpoint", "create", "--task-id", "stale", "--task", "Stale", "--next", "Continue")
	if err == nil || !strings.Contains(err.Error(), "restart the client") || !strings.Contains(err.Error(), fmt.Sprintf("writer API %d", newer)) {
		t.Fatalf("stale process wrote a checkpoint: %v %s", err, output)
	}
	// An ordinary open never lowers the floor; a deliberate rollback can.
	runCLI(t, "", "database", "writer-floor", "--set", fmt.Sprint(buildinfo.WriterAPIVersion))
	runCLI(t, "", "checkpoint", "create", "--task-id", "current", "--task", "Current", "--next", "Continue")
}

func TestRunningMCPServerRefusesWritesAfterWriterUpgrade(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "floor", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	capture := func() *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_item_capture", Arguments: map[string]any{"kind": "idea", "title": "Idea", "body": "Body", "creator": "test"}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := capture(); result.IsError {
		t.Fatalf("current server refused a write: %#v", result.Content)
	}
	// A write-incompatible build opens the store while this server keeps running.
	runCLI(t, "", "database", "writer-floor", "--set", fmt.Sprint(buildinfo.WriterAPIVersion+1))
	result := capture()
	if !result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "restart the client") {
		t.Fatalf("stale server wrote after an upgrade: %#v", result.Content)
	}
	dryRun, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: map[string]any{
		"dry_run": true, "task_id": "recovered", "task": "Recovered handoff", "summary": "Validated while stale", "next_action": "Apply after restart",
	}})
	if err != nil || dryRun.IsError {
		t.Fatalf("stale server refused checkpoint dry run: %v %#v", err, dryRun)
	}
	deferred, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_checkpoint", Arguments: map[string]any{
		"session_id": "recovered-session", "task_id": "recovered", "request_key": "stale-recovery", "task": "Recovered handoff", "summary": "Persist this exact payload", "next_action": "Apply after restart",
	}})
	if err != nil || deferred.IsError {
		t.Fatalf("stale checkpoint was not staged: %v %#v", err, deferred)
	}
	body, err := json.Marshal(deferred.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Deferred   bool   `json:"deferred"`
		RecoveryID string `json:"recovery_id"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Deferred || receipt.RecoveryID == "" || receipt.ID != "" {
		t.Fatalf("stale checkpoint receipt = %s", body)
	}
	if got := runCLI(t, "", "checkpoint", "recovery", "list", "--output", "json"); !strings.Contains(got, receipt.RecoveryID) || !strings.Contains(got, "Recovered handoff") {
		t.Fatalf("staged checkpoint not listed: %s", got)
	}
	if err, _ := runCLIError("checkpoint", "recovery", "apply", receipt.RecoveryID); err == nil || !strings.Contains(err.Error(), "restart the client") {
		t.Fatalf("stale CLI unexpectedly applied recovery: %v", err)
	}
	if got := runCLI(t, "", "checkpoint", "recovery", "list", "--output", "json"); !strings.Contains(got, receipt.RecoveryID) {
		t.Fatalf("failed apply did not retain recovery: %s", got)
	}
	read, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "continuity_bootstrap", Arguments: map[string]any{"full": true}})
	if err != nil || read.IsError {
		t.Fatalf("stale server refused a read: %v %#v", err, read)
	}
	readBody, err := json.Marshal(read.StructuredContent)
	if err != nil || !strings.Contains(string(readBody), receipt.RecoveryID) || !strings.Contains(string(readBody), "Checkpoint recovery") {
		t.Fatalf("bootstrap did not surface pending recovery: %v %s", err, readBody)
	}
	// Simulate the restarted compatible writer. Applying verifies readback before
	// deleting the envelope, so this is the durable handoff boundary.
	runCLI(t, "", "database", "writer-floor", "--set", fmt.Sprint(buildinfo.WriterAPIVersion))
	runCLI(t, `{"session_id":"recovered-session","hook_event_name":"Stop"}`, "hook", "codex")
	applied := runCLI(t, "", "checkpoint", "recovery", "apply", receipt.RecoveryID)
	if !strings.Contains(applied, `"readback_verified": true`) || !strings.Contains(applied, `"recovery_removed": true`) {
		t.Fatalf("recovery was not confirmed: %s", applied)
	}
	if got := runCLI(t, "", "checkpoint", "recovery", "list", "--output", "json"); !strings.Contains(got, `"recoveries": []`) {
		t.Fatalf("applied recovery remains pending: %s", got)
	}
	if got := runCLI(t, "", "checkpoint", "show", "--query", "Persist this exact payload", "--output", "json"); !strings.Contains(got, "Recovered handoff") {
		t.Fatalf("recovered checkpoint was not readable: %s", got)
	}
	if got := runCLI(t, "", "presence", "--session", "recovered-session", "--output", "json"); !strings.Contains(got, `"derived_state": "yielded"`) || strings.Contains(got, `"derived_state": "active"`) {
		t.Fatalf("recovery revived the stopped historical session: %s", got)
	}
}
