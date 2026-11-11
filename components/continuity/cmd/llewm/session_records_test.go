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

// Each client session runs its own MCP process, so records written mid-task
// link to that session's task, and a checkpoint lists only its own unlinked
// records, even while another session works on a different task.
func TestMCPSessionLinksRecordsToItsTaskAndReportsUnlinked(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connect := func(name string) *mcp.ClientSession {
		t.Helper()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPProcess$")
		child.Dir = t.TempDir()
		child.Env = append(os.Environ(), "LLEWM_TEST_MCP_PROCESS=1", "LLEWM_TEST_PROJECT_ROOT="+root)
		session, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	call := func(session *mcp.ClientSession, tool string, arguments map[string]any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if result.IsError {
			t.Fatalf("%s: %s", tool, text)
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatalf("%s: %v %s", tool, err, text)
		}
		return value
	}
	alpha, beta := connect("alpha"), connect("beta")
	call(alpha, "continuity_lease", map[string]any{"action": "acquire", "scope": "task:alpha", "holder": "agent-a", "session": "session-a"})
	call(beta, "continuity_lease", map[string]any{"action": "acquire", "scope": "task:beta", "holder": "agent-b", "session": "session-b"})

	narrative := call(alpha, "continuity_narrative_record", map[string]any{"title": "Finding", "creator": "agent-a", "trigger": "manual", "claims": []map[string]any{{"kind": "outcome", "text": "Observed the finding", "authority": "caller_asserted"}}})
	page := call(alpha, "continuity_history_search", map[string]any{"linked": "task:alpha"})
	if hits, _ := page["hits"].([]any); len(hits) != 1 || !strings.HasPrefix(hits[0].(map[string]any)["ref"].(string), narrative["ref"].(string)+"#") {
		t.Fatalf("narrative was not anchored to the session's task: %v", page)
	}
	item := call(beta, "continuity_item_capture", map[string]any{"kind": "task", "title": "Follow up", "body": "Later", "creator": "agent-b"})
	shown := call(beta, "continuity_item", map[string]any{"id": item["ref"]})
	if got := shown["item"].(map[string]any)["item"].(map[string]any)["task_id"]; got != "beta" {
		t.Fatalf("item task = %v, want the session's task beta", got)
	}
	decision := call(alpha, "continuity_decision_record", map[string]any{"id": "pick", "title": "Pick", "rationale": "The user chose", "disposition": "accepted", "actor": "owner", "actor_type": "human", "authority": "user_reported", "review_source": "chat"})

	tip := ""
	checkpoint := func(session *mcp.ClientSession, task string, dryRun bool, evidence []string) []any {
		t.Helper()
		output := call(session, "continuity_checkpoint", map[string]any{"dry_run": dryRun, "task_id": task, "task": "Work " + task, "summary": "Progress", "next_action": "Continue", "evidence_refs": evidence, "predecessor_checkpoint_id": tip})
		if id, _ := output["id"].(string); id != "" {
			tip = id
		}
		unlinked, _ := output["unlinked_records"].([]any)
		return unlinked
	}
	// Only alpha's decision is unlinked: its narrative is anchored, and beta's
	// item belongs to the other session.
	if unlinked := checkpoint(alpha, "alpha", true, nil); len(unlinked) != 1 || unlinked[0] != decision["ref"] {
		t.Fatalf("alpha unlinked = %v, want only %v", unlinked, decision["ref"])
	}
	if unlinked := checkpoint(beta, "beta", true, nil); len(unlinked) != 0 {
		t.Fatalf("beta unlinked = %v", unlinked)
	}
	if unlinked := checkpoint(alpha, "alpha", true, []string{decision["ref"].(string)}); len(unlinked) != 0 {
		t.Fatalf("evidence ref did not link the decision: %v", unlinked)
	}
	// A saved checkpoint starts a new interval.
	if unlinked := checkpoint(alpha, "alpha", false, nil); len(unlinked) != 1 {
		t.Fatalf("save unlinked = %v", unlinked)
	}
	if unlinked := checkpoint(alpha, "alpha", true, nil); len(unlinked) != 0 {
		t.Fatalf("interval was not reset after save: %v", unlinked)
	}
}
