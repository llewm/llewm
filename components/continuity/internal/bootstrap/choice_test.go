package bootstrap

import (
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

func TestChooseOrdersPresenceThenBranchThenNewest(t *testing.T) {
	now := time.Now()
	tips := []checkpoint.TaskTip{
		{TaskID: "newest", Status: "in_progress", CreatedAt: now, Branches: []checkpoint.CodeBranch{{Branch: "other"}}},
		{TaskID: "feature", Status: "in_progress", CreatedAt: now.Add(-time.Hour), Branches: []checkpoint.CodeBranch{{Branch: "feature/x"}}},
		{TaskID: "older-feature", Status: "paused", CreatedAt: now.Add(-2 * time.Hour), Branches: []checkpoint.CodeBranch{{Branch: "feature/x"}}},
		{TaskID: "present", Status: "blocked", CreatedAt: now.Add(-3 * time.Hour)},
	}
	current := []checkpoint.CodeBranch{{Branch: "feature/x"}}
	// Two days later no task contends, so the ordering rules apply alone.
	later := ChooseContext{Now: now.Add(48 * time.Hour)}

	present := Choose(tips, []string{"no-checkpoint", "present"}, current, later)
	if present.Rule != RulePresence || present.Selection.TaskID != "present" {
		t.Fatalf("presence rule: %#v", present)
	}
	branch := Choose(tips, []string{"no-checkpoint"}, current, later)
	if branch.Rule != RuleBranch || branch.Selection.TaskID != "feature" || !strings.Contains(branch.Detail, "`feature/x`") {
		t.Fatalf("branch rule picked %#v, want the newest task on feature/x", branch)
	}
	// A member's branch matches only that member's recorded branch.
	if member := Choose(tips, nil, []checkpoint.CodeBranch{{Member: "lib", Branch: "feature/x"}}, later); member.Rule != RuleNewest {
		t.Fatalf("member branch matched the primary repository: %#v", member)
	}
	newest := Choose(tips, nil, []checkpoint.CodeBranch{{Branch: "main"}}, later)
	if newest.Rule != RuleNewest || newest.Selection.TaskID != "newest" || !strings.Contains(newest.Detail, "`main`") {
		t.Fatalf("newest rule: %#v", newest)
	}
	// A detached HEAD matches nothing, not even detached checkpoints.
	if detached := Choose([]checkpoint.TaskTip{{TaskID: "d", Branches: []checkpoint.CodeBranch{{}}}}, nil, []checkpoint.CodeBranch{{}}, later); detached.Rule != RuleNewest || detached.Detail != "no presence or current-branch match; newest unfinished task" {
		t.Fatalf("detached rule: %#v", detached)
	}
}

func TestRestartHandoffSelection(t *testing.T) {
	now := time.Now().UTC()
	marker := &checkpoint.RestartResume{InstanceID: "device-a", Client: "codex"}
	tips := []checkpoint.TaskTip{
		{TaskID: "restart", Status: "in_progress", CreatedAt: now, RestartResume: marker},
		{TaskID: "other", Status: "in_progress", CreatedAt: now},
	}
	ctx := ChooseContext{Now: now, RestartInstance: "device-a", RestartClient: "codex"}
	if got := Choose(tips, nil, nil, ctx); got.Rule != RuleRestart || got.Selection.TaskID != "restart" {
		t.Fatalf("restart: %+v", got)
	}
	if got := Choose(tips, []string{"other"}, nil, ctx); got.Rule != RulePresence {
		t.Fatalf("presence precedence: %+v", got)
	}
	ctx.RestartInstance = "device-b"
	if got := Choose(tips, nil, nil, ctx); got.Rule != RuleAmbiguous {
		t.Fatalf("different device: %+v", got)
	}
	ctx.RestartInstance, ctx.RestartClient = "device-a", "claude-code"
	if got := Choose(tips, nil, nil, ctx); got.Rule != RuleAmbiguous {
		t.Fatalf("different client: %+v", got)
	}
	ctx.RestartClient = "codex"
	tips[1].RestartResume = marker
	if got := Choose(tips, nil, nil, ctx); got.Rule != RuleAmbiguous || len(got.Others) != 2 {
		t.Fatalf("competing handoffs: %+v", got)
	}
	tips[0].Status = "completed"
	if got := Choose(tips, nil, nil, ctx); got.Rule != RuleRestart || got.Selection.TaskID != "other" {
		t.Fatalf("completed marker: %+v", got)
	}
	tips[1].RestartResume = nil
	if got := Choose(tips, nil, nil, ctx); got.Rule == RuleRestart {
		t.Fatalf("new tip clears marker: %+v", got)
	}
}

func TestChooseAsksInsteadOfTakingAnotherAgentsTask(t *testing.T) {
	now := time.Now()
	main := []checkpoint.CodeBranch{{Branch: "main"}}
	tips := []checkpoint.TaskTip{
		{TaskID: "claude-task", Status: "in_progress", CreatedAt: now.Add(-time.Minute), Branches: main},
		{TaskID: "codex-task", Status: "in_progress", CreatedAt: now.Add(-time.Hour), Branches: main},
		{TaskID: "set-aside", Status: "paused", CreatedAt: now.Add(-time.Hour), Branches: main},
		{TaskID: "stale", Status: "in_progress", CreatedAt: now.Add(-72 * time.Hour), Branches: main},
	}
	fresh := Choose(tips, nil, main, ChooseContext{Now: now})
	if fresh.Rule != RuleAmbiguous || fresh.Selection != (checkpoint.Selection{}) || len(fresh.Others) != 2 || fresh.Others[0].TaskID != "claude-task" || fresh.Others[1].TaskID != "codex-task" {
		t.Fatalf("fresh session took a contended task: %#v", fresh)
	}
	line := ChoiceLine(fresh)
	for _, want := range []string{"No task chosen: 2 tasks are in progress on the current branch `main`", "Ask the user", "`codex-task` (in_progress, on `main`)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("ambiguous line lacks %q: %s", want, line)
		}
	}
	// The session that last checkpointed codex-task resumes it, even though
	// claude-task is newer on the same branch.
	if resumed := Choose(tips, nil, main, ChooseContext{SessionTask: "codex-task", Now: now}); resumed.Rule != RuleSession || resumed.Selection.TaskID != "codex-task" {
		t.Fatalf("session affinity: %#v", resumed)
	}
	// Live presence still wins over session affinity.
	if present := Choose(tips, []string{"claude-task"}, main, ChooseContext{SessionTask: "codex-task", Now: now}); present.Rule != RulePresence || present.Selection.TaskID != "claude-task" {
		t.Fatalf("presence precedence: %#v", present)
	}
	// Closing a lineage, pausing, or going stale removes a contender.
	closed := func(taskID string) bool { return taskID == "codex-task" }
	if single := Choose(tips, nil, main, ChooseContext{Now: now, Closed: closed}); single.Rule != RuleBranch || single.Selection.TaskID != "claude-task" {
		t.Fatalf("single in-progress task was not chosen: %#v", single)
	}
	// A newer completed task never displaces an unfinished one.
	finished := append([]checkpoint.TaskTip{{TaskID: "done", Status: "completed", CreatedAt: now, Branches: main}}, tips[1:]...)
	finishedClosed := func(taskID string) bool { return taskID == "claude-task" }
	if kept := Choose(finished, nil, main, ChooseContext{Now: now, Closed: finishedClosed}); kept.Rule != RuleBranch || kept.Selection.TaskID != "codex-task" {
		t.Fatalf("completed task displaced unfinished work on the branch: %#v", kept)
	}
	if kept := Choose(finished, nil, []checkpoint.CodeBranch{{Branch: "topic"}}, ChooseContext{Now: now, Closed: finishedClosed}); kept.Rule != RuleNewest || kept.Selection.TaskID != "codex-task" {
		t.Fatalf("completed task displaced unfinished work project-wide: %#v", kept)
	}
	// A task with another live session present is skipped, which also ends contention.
	heldByCodex := func(taskID string) string {
		if taskID == "codex-task" {
			return "codex (session c1)"
		}
		return ""
	}
	if free := Choose(tips, nil, main, ChooseContext{Now: now, PresentByOthers: heldByCodex}); free.Rule != RuleBranch || free.Selection.TaskID != "claude-task" {
		t.Fatalf("present task was not skipped: %#v", free)
	}
	allHeld := func(taskID string) string { return "someone" }
	if asked := Choose(tips, nil, main, ChooseContext{Now: now, PresentByOthers: allHeld}); asked.Rule != RuleAmbiguous || strings.Contains(asked.Detail, "another live session present") {
		t.Fatalf("all-present tasks did not ask: %#v", asked)
	}
	// A sole abandoned session's unexpired observation cannot block recovery.
	for _, branches := range [][]checkpoint.CodeBranch{main, {{Branch: "topic"}}} {
		resumed := Choose(tips[1:2], nil, branches, ChooseContext{Now: now, PresentByOthers: heldByCodex})
		if resumed.Selection.TaskID != "codex-task" || resumed.Rule == RuleAmbiguous || resumed.RecentPresence != "codex (session c1)" {
			t.Fatalf("sole task blocked by an unexpired observation: %#v", resumed)
		}
		if line := ChoiceLine(resumed); !strings.Contains(line, "does not prove a current writer") || strings.Contains(line, "Task selection needed") {
			t.Fatalf("sole task lost its advisory: %s", line)
		}
	}
	// Without a branch match the project-wide newest rule asks too.
	if offBranch := Choose(tips, nil, []checkpoint.CodeBranch{{Branch: "topic"}}, ChooseContext{Now: now}); offBranch.Rule != RuleAmbiguous || !strings.Contains(offBranch.Detail, "in this project") {
		t.Fatalf("newest rule took a contended task: %#v", offBranch)
	}
}

func TestOtherActiveAndChoiceLine(t *testing.T) {
	now := time.Now()
	tips := []checkpoint.TaskTip{
		{TaskID: "chosen", Status: "in_progress", CreatedAt: now},
		{TaskID: "done", Status: "completed", CreatedAt: now},
		{TaskID: "closed", Status: "in_progress", CreatedAt: now},
		{TaskID: "a", Status: "blocked", CreatedAt: now, Branches: []checkpoint.CodeBranch{{Branch: "topic"}}},
		{TaskID: "b", Status: "in_progress", CreatedAt: now, Branches: []checkpoint.CodeBranch{{}}},
		{TaskID: "c", Status: "paused", CreatedAt: now, Branches: []checkpoint.CodeBranch{{Member: "lib", Branch: "x"}}},
		{TaskID: "d", Status: "in_progress", CreatedAt: now},
	}
	others, omitted := OtherActive(tips, "chosen", func(taskID string) bool { return taskID == "closed" })
	if len(others) != 3 || omitted != 1 || others[0].TaskID != "a" || others[0].Branch != "topic" || !others[1].Detached || others[2].Branch != "lib@x" {
		t.Fatalf("others: %#v omitted %d", others, omitted)
	}
	line := ChoiceLine(Choice{Rule: RuleBranch, Detail: "its latest checkpoint was on the current branch `topic`", Others: others, OthersOmitted: omitted})
	for _, want := range []string{"Chosen by branch:", "Order: explicit, presence, session, restart handoff, current branch, newest.", "`a` (blocked, on `topic`)", "`b` (in_progress, detached HEAD)", "`c` (paused, on `lib@x`)", "and 1 more", "--task-id"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	if line := ChoiceLine(Choice{Rule: RuleExplicit}); line != "Chosen by explicit selection. Order: explicit, presence, session, restart handoff, current branch, newest." {
		t.Fatalf("explicit line: %s", line)
	}
}

func TestChooseDoesNotResumeTerminalAffinityOrCompletedFallback(t *testing.T) {
	now := time.Now()
	main := []checkpoint.CodeBranch{{Branch: "main"}}
	tips := []checkpoint.TaskTip{
		{TaskID: "completed", Status: "completed", CreatedAt: now, Branches: main},
		{TaskID: "closed", Status: "paused", CreatedAt: now.Add(-time.Minute), Branches: main},
		{TaskID: "open", Status: "paused", CreatedAt: now.Add(-2 * time.Minute), Branches: main},
	}
	closed := func(taskID string) bool { return taskID == "closed" }
	choice := Choose(tips, []string{"completed", "closed"}, main, ChooseContext{SessionTask: "completed", Now: now, Closed: closed})
	if choice.Rule != RuleBranch || choice.Selection.TaskID != "open" {
		t.Fatalf("terminal affinity displaced open work: %#v", choice)
	}
	none := Choose(tips[:2], []string{"completed", "closed"}, main, ChooseContext{SessionTask: "completed", Now: now, Closed: closed})
	if none.Selection != (checkpoint.Selection{}) {
		t.Fatalf("terminal task was selected as fallback: %#v", none)
	}
}
