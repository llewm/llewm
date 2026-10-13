package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksAttributeAgentCommitsToTheirTask(t *testing.T) {
	root := testProject(t)
	setTestAuthor(t, root)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "review", "--session", "reviewer", "--task", "Review", "--next", "Fix what the review found")
	tool := func(session, command string) {
		t.Helper()
		runCLI(t, `{"session_id":"`+session+`","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"`+command+`"}}`, "hook", "claude-code")
	}
	tool("reviewer", "git status")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "Fix the reviewed bug")
	tool("reviewer", "git commit -m fix")
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "Hand-made commit")
	tool("reviewer", "ls")

	start := runCLI(t, `{"session_id":"owner","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	if !strings.Contains(start, "Fix the reviewed bug (task review)") || !strings.Contains(start, "a commit attributed to another task belongs to that task's agent") {
		t.Fatalf("agent commit not attributed to its task:\n%s", start)
	}
	if strings.Contains(start, "Hand-made commit (") {
		t.Fatalf("a commit made outside any tool call was attributed:\n%s", start)
	}
}

func TestOwnerIsToldWhenAnotherAgentChangesItsFiles(t *testing.T) {
	root := testProject(t)
	setTestAuthor(t, root)
	runCLI(t, "", "init")
	if err := os.WriteFile(filepath.Join(root, "engine.go"), []byte("package engine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "engine.go")
	gitIn(t, root, "commit", "-q", "-m", "Add engine")
	runCLI(t, "", "checkpoint", "create", "--task-id", "owner", "--session", "owner-session", "--task", "Build the engine", "--change", "engine.go", "--next", "Finish the engine")

	// A reviewer works on the same file under its own task and commits a fix.
	runCLI(t, "", "checkpoint", "create", "--task-id", "review", "--session", "reviewer", "--task", "Review the engine", "--change", "engine.go", "--next", "Fix the bug")
	runCLI(t, `{"session_id":"reviewer","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}`, "hook", "claude-code")
	if err := os.WriteFile(filepath.Join(root, "engine.go"), []byte("package engine\n\n// fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "commit", "-q", "-am", "Fix the engine bug")
	runCLI(t, `{"session_id":"reviewer","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"git commit -am fix"}}`, "hook", "claude-code")

	owner := runCLI(t, "", "bootstrap", "--task-id", "owner")
	if !strings.Contains(owner, "**Changed by others (machine-observed):** Since this task's last checkpoint, 1 commit(s) by others changed files it touched:") || !strings.Contains(owner, `"Fix the engine bug" (task review): engine.go`) {
		t.Fatalf("owner was not told about the reviewer's commit:\n%s", owner)
	}
	if !strings.Contains(owner, "**Shared files (machine-observed):** Files this task touched are also in the work of other in-progress tasks: `review`: engine.go") {
		t.Fatalf("owner was not told about the shared file:\n%s", owner)
	}
	// The reviewer's own commit is not reported back to the reviewer as foreign.
	if review := runCLI(t, "", "bootstrap", "--task-id", "review"); strings.Contains(review, "Changed by others") || !strings.Contains(review, "`owner`: engine.go") {
		t.Fatalf("reviewer notices wrong:\n%s", review)
	}
}

func TestReviewLinksShowInBothTasks(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "owner", "--task", "Build the engine", "--next", "Finish the engine")
	if err, output := runCLIError("checkpoint", "create", "--task-id", "review", "--reviews-task-id", "missing", "--task", "Review", "--next", "Review"); err == nil || !strings.Contains(err.Error()+output, "has no checkpoints") {
		t.Fatalf("review of an unknown task was accepted: %v %s", err, output)
	}
	if err, output := runCLIError("checkpoint", "create", "--task-id", "owner-self", "--reviews-task-id", "owner-self", "--task", "Self", "--next", "Review"); err == nil || !strings.Contains(err.Error()+output, "cannot review itself") {
		t.Fatalf("self-review was accepted: %v %s", err, output)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "review", "--reviews-task-id", "owner", "--task", "Review the engine", "--summary", "Found a comma-splitting bug in the flags", "--next", "Fix it")
	owner := runCLI(t, "", "bootstrap", "--task-id", "owner")
	if !strings.Contains(owner, "**Reviews (caller-asserted links):** reviewed by `review` (in_progress, checkpoint ") || !strings.Contains(owner, "Found a comma-splitting bug in the flags") {
		t.Fatalf("owner does not see its reviewer:\n%s", owner)
	}
	review := runCLI(t, "", "bootstrap", "--task-id", "review")
	if !strings.Contains(review, "**Reviews (caller-asserted links):** this task reviews `owner` (in_progress; tip ") {
		t.Fatalf("reviewer does not see what it reviews:\n%s", review)
	}
}
