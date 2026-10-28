package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckpointsLeaseTheirTaskToTheSession(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	save := func(args ...string) string {
		t.Helper()
		var receipt struct {
			SessionLease string `json:"session_lease"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, "", append(append([]string{"checkpoint", "create"}, args...), "--output", "json")...)), &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt.SessionLease
	}
	if got := save("--task-id", "a-work", "--task", "A", "--next", "Continue", "--session", "agent-a"); !strings.Contains(got, "task:a-work leased to this session until") {
		t.Fatalf("first checkpoint did not lease its task: %q", got)
	}
	if bootstrap := runCLI(t, "", "bootstrap"); !strings.Contains(bootstrap, "- Task ID: a-work\n") || strings.Contains(bootstrap, "## Task selection needed") || !strings.Contains(bootstrap, "does not prove a current writer") {
		t.Fatalf("sole task was blocked by abandoned-session presence:\n%s", bootstrap)
	}
	if got := save("--task-id", "a-work", "--predecessor-checkpoint", taskTip(t, "a-work"), "--task", "A", "--next", "Continue", "--session", "agent-a"); !strings.Contains(got, "(token 1)") {
		t.Fatalf("a later checkpoint did not renew the same lease: %q", got)
	}
	// Another agent may take over at the tip, but is told who holds the task.
	if got := save("--task-id", "a-work", "--predecessor-checkpoint", taskTip(t, "a-work"), "--task", "A", "--next", "Continue", "--session", "agent-b"); !strings.Contains(got, "is leased by") || !strings.Contains(got, "session agent-a") {
		t.Fatalf("takeover was not told about the holder: %q", got)
	}
	// Multiple recent tasks remain ambiguous; neither a checkpoint observation
	// nor a coordination lease establishes that another client is still running.
	save("--task-id", "b-work", "--task", "B", "--next", "Continue", "--session", "agent-b")
	if bootstrap := runCLI(t, "", "bootstrap"); !strings.Contains(bootstrap, "## Task selection needed") || strings.Contains(bootstrap, "another live session present") || strings.Contains(bootstrap, "leased by another live session") {
		t.Fatalf("fresh session ignored live presence or treated a lease as presence:\n%s", bootstrap)
	}
	// Completing the task releases the session's lease.
	if got := save("--task-id", "b-work", "--predecessor-checkpoint", taskTip(t, "b-work"), "--task", "B", "--status", "completed", "--next", "Task complete; await the next objective.", "--action-updates-json", `[{"id":"`+strings.Fields(runCLI(t, "", "checkpoint", "actions", "--task-id", "b-work"))[0]+`","status":"completed"}]`, "--session", "agent-b"); !strings.Contains(got, "released task:b-work") {
		t.Fatalf("completed checkpoint kept its lease: %q", got)
	}
}

func TestSessionEndReleasesOnlyEndingSessionsLeases(t *testing.T) {
	for _, client := range []string{"codex", "claude-code"} {
		t.Run(client, func(t *testing.T) {
			testProject(t)
			runCLI(t, "", "init")
			runCLI(t, "", "checkpoint", "create", "--task-id", "ending-task", "--task", "Continue sync", "--next", "Continue", "--session", "ending")
			runCLI(t, "", "lease", "acquire", "track_plan:ending-plan", "--holder", client, "--session", "ending")
			runCLI(t, "", "lease", "acquire", "task:other-task", "--holder", client, "--session", "other")
			hook := func(event string) {
				runCLI(t, `{"session_id":"ending","hook_event_name":"`+event+`"}`, "hook", client)
			}
			check := func(want int) {
				t.Helper()
				var values []struct {
					Session string `json:"session_id"`
				}
				if err := json.Unmarshal([]byte(runCLI(t, "", "lease", "list", "--output", "json")), &values); err != nil {
					t.Fatal(err)
				}
				if len(values) != want || (want == 1 && values[0].Session != "other") {
					t.Fatalf("remaining leases: %+v; want %d", values, want)
				}
			}
			hook("Stop")
			check(3)
			hook("SessionEnd")
			check(1)
			// Duplicate delivery is harmless and preserves task recovery.
			hook("SessionEnd")
			check(1)
			if got := runCLI(t, "", "checkpoint", "show", taskTip(t, "ending-task")); !strings.Contains(got, "Continue sync") {
				t.Fatalf("session exit lost task: %s", got)
			}
		})
	}
}

func TestRestartCheckpointReleasesLeaseWithoutCompletingTask(t *testing.T) {
	testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init")
	runCLI(t, `{"session_id":"before","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	runCLI(t, "", "checkpoint", "create", "--task-id", "restart-task", "--task", "Continue sync", "--next", "Continue", "--session", "before")
	output := runCLI(t, "", "checkpoint", "create", "--task-id", "restart-task", "--predecessor-checkpoint", taskTip(t, "restart-task"), "--task", "Continue sync", "--next", "Verify updated runtime then resume sync work", "--session", "before", "--resume-after-restart", "--output", "json")
	if !strings.Contains(output, "released task:restart-task: the session is handing off for restart") {
		t.Fatalf("restart retained lease: %s", output)
	}
	if output := runCLI(t, "", "lease", "list", "--output", "json"); strings.TrimSpace(output) != "[]" {
		t.Fatalf("restart retained lease: %s", output)
	}
	// The next session takes a fresh token on the same task and tip; the task
	// remains in progress and does not need the old session's TTL to expire.
	output = runCLI(t, "", "checkpoint", "create", "--task-id", "restart-task", "--predecessor-checkpoint", taskTip(t, "restart-task"), "--task", "Continue sync", "--next", "Continue optimization", "--session", "after", "--output", "json")
	if !strings.Contains(output, "(token 2)") || strings.Contains(output, "is leased by") {
		t.Fatalf("fresh session did not recover ownership: %s", output)
	}
}
