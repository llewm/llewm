package app

import (
	"context"
	"database/sql"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

func TestCommitAfterFinishedTaskIsNotCreditedToIt(t *testing.T) {
	ctx := context.Background()
	a := fenceApp(t)
	a.Presence = presence.Service{DB: a.DB}
	a.Repositories = project.Repositories{Root: a.Root}
	gitRun(t, a.Root, "init", "-q", "-b", "main")
	gitRun(t, a.Root, "commit", "-q", "--allow-empty", "-m", "base")
	a.ObserveHeads(ctx, "claude-code", "s", "sessionstart", "", nil)
	if _, err := a.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "done", Task: "Done", Summary: "Finished", NextAction: "None", Status: "completed", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	commitTask := func(message string) string {
		t.Helper()
		gitRun(t, a.Root, "commit", "-q", "--allow-empty", "-m", message)
		a.ObserveHeads(ctx, "claude-code", "s", "posttooluse", "Bash", map[string]any{"command": "git commit -m " + message})
		var task sql.NullString
		if err := a.DB.QueryRow(`SELECT task_id FROM commit_authors WHERE session_id='s' AND commit_sha=?`, gitRun(t, a.Root, "rev-parse", "HEAD")).Scan(&task); err != nil {
			t.Fatalf("commit %q not attributed to the session: %v", message, err)
		}
		return task.String
	}
	if task := commitTask("untracked work"); task != "" {
		t.Fatalf("commit after the session finished its task was credited to %q", task)
	}
	untracked := gitRun(t, a.Root, "rev-parse", "HEAD")
	if _, err := a.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "next", Task: "Next", Summary: "Working", NextAction: "Continue", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if task := commitTask("tracked work"); task != "next" {
		t.Fatalf("commit for the in-progress task was credited to %q", task)
	}
	tracked := gitRun(t, a.Root, "rev-parse", "HEAD")
	// The checkpoint that followed the task-less commit names its task, as an
	// inference; the observed commit keeps its observed task.
	authors, err := authorship.Authors(ctx, a.DB, "p", "", []string{untracked, tracked})
	if err != nil {
		t.Fatal(err)
	}
	if got := authors[untracked]; got.TaskID != "next" || !got.TaskInferred || got.SessionID != "s" {
		t.Fatalf("task-less commit after the next checkpoint = %+v", got)
	}
	if got := authors[tracked]; got.TaskID != "next" || got.TaskInferred {
		t.Fatalf("observed commit = %+v", got)
	}
	if _, err := a.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "other", Task: "Other", Summary: "Working", NextAction: "Continue", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if authors, err = authorship.Authors(ctx, a.DB, "p", "", []string{untracked}); err != nil || authors[untracked].TaskID != "next" {
		t.Fatalf("a later checkpoint retasked an earlier commit: %+v %v", authors, err)
	}
}
