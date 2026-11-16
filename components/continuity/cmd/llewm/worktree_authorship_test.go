package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/app"
)

// A session in a linked worktree shares the main checkout's store but has its
// own HEAD: its hooks, checkpoints and bootstrap must read that HEAD, not the
// main checkout's.
func TestLinkedWorktreeSessionReadsItsOwnHead(t *testing.T) {
	root := testProject(t)
	setTestAuthor(t, root)
	runCLI(t, "", "init")
	worktree := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt")
	gitIn(t, root, "worktree", "add", "-q", "-b", "side", worktree)
	tool := func(session, command string) {
		t.Helper()
		runCLI(t, `{"session_id":"`+session+`","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"`+command+`"}}`, "hook", "claude-code")
	}

	t.Chdir(root)
	tool("main-session", "git status")
	t.Chdir(worktree)
	runCLI(t, "", "checkpoint", "create", "--task-id", "wt-task", "--session", "wt-session", "--task", "Worktree work", "--next", "Commit")
	tool("wt-session", "git status")
	gitIn(t, worktree, "commit", "-q", "--allow-empty", "-m", "First worktree commit")
	first := gitIn(t, worktree, "rev-parse", "HEAD")
	tool("wt-session", "git commit -m first")
	// An unhooked commit in the main checkout, then the worktree session's next
	// commit: only the worktree's own commit is the worktree session's.
	gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "Hand-made main commit")
	mainCommit := gitIn(t, root, "rev-parse", "HEAD")
	gitIn(t, worktree, "commit", "-q", "--allow-empty", "-m", "Second worktree commit")
	second := gitIn(t, worktree, "rev-parse", "HEAD")
	tool("wt-session", "git commit -m second")
	runCLI(t, "", "checkpoint", "create", "--task-id", "wt-task", "--predecessor-checkpoint", taskTip(t, "wt-task"), "--session", "wt-session", "--task", "Worktree work", "--next", "Review")

	a, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	authors := map[string]string{}
	rows, err := a.DB.Query(`SELECT commit_sha, session_id FROM commit_authors`)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	for rows.Next() {
		var commit, session string
		if err := rows.Scan(&commit, &session); err != nil {
			t.Fatal(err)
		}
		authors[commit] = session
	}
	rows.Close()
	var mainHead string
	if err := a.DB.QueryRow(`SELECT head FROM head_observations WHERE member=''`).Scan(&mainHead); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if authors[first] != "wt-session" || authors[second] != "wt-session" {
		t.Fatalf("worktree commits not credited to the worktree session: %v", authors)
	}
	if _, ok := authors[mainCommit]; ok {
		t.Fatalf("unhooked main-checkout commit was credited: %v", authors)
	}
	if mainHead == second || mainHead == first {
		t.Fatalf("the worktree's HEAD replaced the main checkout's observation: %s", mainHead)
	}

	// The checkpoint recorded the worktree's HEAD and branch, so the worktree
	// is not behind it, while the main checkout, which lacks those commits, is.
	inWorktree := runCLI(t, "", "bootstrap", "--task-id", "wt-task")
	if strings.Contains(inWorktree, "**Code check") {
		t.Fatalf("worktree reported behind its own checkpoint:\n%s", inWorktree)
	}
	start := runCLI(t, `{"session_id":"other","hook_event_name":"SessionStart","source":"startup"}`, "hook", "codex")
	if !strings.Contains(start, "Second worktree commit (task wt-task)") || strings.Contains(start, "Hand-made main commit") {
		t.Fatalf("worktree session start does not list the worktree's own unnarrated commits:\n%s", start)
	}
	t.Chdir(root)
	// The test commits are empty, so their changes count as landed anywhere;
	// what matters is that the main checkout's HEAD lacks the recorded commit.
	if inMain := runCLI(t, "", "bootstrap", "--task-id", "wt-task"); !strings.Contains(inMain, "written on "+second[:12]+", which HEAD does not contain") {
		t.Fatalf("main checkout did not notice it lacks the worktree's commits:\n%s", inMain)
	}
}
