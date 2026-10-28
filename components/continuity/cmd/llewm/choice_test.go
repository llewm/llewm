package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestBootstrapChoosesTaskByPresenceSessionBranchOrAsks(t *testing.T) {
	root := testProject(t)
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runCLI(t, "", "init")
	defaultBranch := git("symbolic-ref", "--short", "HEAD")
	runCLI(t, "", "checkpoint", "create", "--task", "Main work", "--task-id", "main-task", "--next", "Continue main")
	git("checkout", "-q", "-b", "feature/x")
	runCLI(t, "", "checkpoint", "create", "--task", "Feature work", "--task-id", "feature-task", "--next", "Continue feature")

	chosen := func(output, taskID, rule string) {
		t.Helper()
		if !strings.Contains(output, "- Task ID: "+taskID+"\n") || !strings.Contains(output, "**Session selection (machine-observed):** "+rule) {
			t.Fatalf("want %s by %q:\n%s", taskID, rule, output)
		}
	}
	hook := func(input string) string {
		t.Helper()
		var output struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, input, "hook", "claude-code")), &output); err != nil {
			t.Fatal(err)
		}
		return output.HookSpecificOutput.AdditionalContext
	}
	chosen(runCLI(t, "", "bootstrap"), "feature-task", "Chosen by branch: its latest checkpoint was on the current branch `feature/x`.")

	// Back on the default branch, its task resumes although another is newer.
	git("checkout", "-q", defaultBranch)
	onDefault := runCLI(t, "", "bootstrap")
	chosen(onDefault, "main-task", "Chosen by branch: its latest checkpoint was on the current branch `"+defaultBranch+"`.")
	if !strings.Contains(onDefault, "Other active tasks: `feature-task` (in_progress, on `feature/x`); select one with --task-id or task_id.") {
		t.Fatalf("other tasks not listed:\n%s", onDefault)
	}

	// The task where a session is live comes before the branch, for that session only.
	runCLI(t, "", "checkpoint", "create", "--task", "Feature work", "--task-id", "feature-task", "--predecessor-checkpoint", taskTip(t, "feature-task"), "--next", "Continue feature", "--session", "s1")
	chosen(hook(`{"session_id":"s1","hook_event_name":"SessionStart","source":"resume"}`), "feature-task", "Chosen by presence: task:feature-task has live presence from this session.")
	chosen(hook(`{"session_id":"s2","hook_event_name":"SessionStart","source":"startup"}`), "main-task", "Chosen by branch:")

	chosen(runCLI(t, "", "bootstrap", "--task-id", "feature-task"), "feature-task", "Chosen by explicit selection.")

	// A detached HEAD matches no branch. Session s1 is still present on feature-task,
	// so a fresh session takes the free main-task rather than s1's work.
	git("checkout", "-q", "--detach")
	chosen(runCLI(t, "", "bootstrap"), "main-task", "Chosen as the project's newest checkpoint")
	// Once the session ends both tasks are free, in progress and recent,
	// so a fresh session asks instead of taking the newest one.
	runCLI(t, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"exit"}`, "hook", "claude-code")
	detached := runCLI(t, "", "bootstrap")
	if !strings.Contains(detached, "## Task selection needed") || strings.Contains(detached, "- Task ID:") || !strings.Contains(detached, "`feature-task` (in_progress") || !strings.Contains(detached, "`main-task` (in_progress, on `"+defaultBranch+"`)") {
		t.Fatalf("contended fresh session did not ask:\n%s", detached)
	}

	// A session that checkpointed a task resumes it, even off its branch and
	// after another task was checkpointed later.
	runCLI(t, "", "checkpoint", "create", "--task", "Main work", "--task-id", "main-task", "--predecessor-checkpoint", taskTip(t, "main-task"), "--next", "Continue main again", "--session", "s3")
	runCLI(t, "", "checkpoint", "create", "--task", "Feature work", "--task-id", "feature-task", "--predecessor-checkpoint", taskTip(t, "feature-task"), "--next", "Continue feature again", "--session", "s4")
	resumed := hook(`{"session_id":"s3","hook_event_name":"SessionStart","source":"compact"}`)
	// The checkpoint recorded exact presence for s3, so the presence rule resumes it.
	chosen(resumed, "main-task", "Chosen by presence: task:main-task has live presence from this session.")
	if !strings.Contains(resumed, "This session: `s3`.") {
		t.Fatalf("hook bootstrap does not name the session:\n%s", resumed)
	}
}

func TestFreshSessionRecoversLastLocallyObservedClientTask(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, `{"session_id":"old","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	runCLI(t, "", "checkpoint", "create", "--task", "Work", "--task-id", "work", "--next", "Continue", "--session", "old")
	// A later handoff of the same task omitted the session identifier.
	runCLI(t, "", "checkpoint", "create", "--task", "Work", "--task-id", "work", "--predecessor-checkpoint", taskTip(t, "work"), "--next", "Continue without a session")
	runCLI(t, "", "checkpoint", "create", "--task", "Old open task", "--task-id", "other", "--next", "Later")
	runCLI(t, `{"session_id":"old","hook_event_name":"SessionEnd","reason":"exit"}`, "hook", "codex")
	hook := runCLI(t, `{"session_id":"new","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	if !strings.Contains(hook, "Task ID: work") || !strings.Contains(hook, "Chosen by local client history") {
		t.Fatalf("fresh client did not recover local task:\n%s", hook)
	}
	// A second observed live session makes the local-history hint ambiguous.
	concurrent := runCLI(t, `{"session_id":"third","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	if !strings.Contains(concurrent, "Task selection needed") {
		t.Fatalf("local task guessed with another live session:\n%s", concurrent)
	}
}
