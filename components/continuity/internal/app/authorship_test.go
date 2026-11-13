package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
	if _, err := a.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "next", Task: "Next", Summary: "Working", NextAction: "Continue", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if task := commitTask("tracked work"); task != "next" {
		t.Fatalf("commit for the in-progress task was credited to %q", task)
	}
}

func TestHookRunsPendingAuthorshipRevalidationOnce(t *testing.T) {
	ctx := context.Background()
	a := fenceApp(t)
	a.Repositories = project.Repositories{Root: a.Root}
	gitRun(t, a.Root, "init", "-q", "-b", "main")
	gitRun(t, a.Root, "commit", "-q", "--allow-empty", "-m", "base")
	pulled := strings.Repeat("a", 40) // no reflog entry ever created it here
	if _, err := a.DB.Exec(`INSERT INTO commit_authors(project_id,member,commit_sha,session_id,client,task_id,tool_name,certainty,observed_at) VALUES ('p','',?,'s','codex','t','Bash','observed','2026-11-12T18:43:04Z');
		INSERT INTO commit_author_revalidations_pending(project_id,cutoff) VALUES ('p','2026-11-13T00:00:00Z')`, pulled); err != nil {
		t.Fatal(err)
	}
	a.ObserveHeads(ctx, "codex", "s", "sessionstart", "", nil)
	var revoked, pending int
	if err := a.DB.QueryRow(`SELECT (SELECT count(*) FROM commit_author_revocations WHERE commit_sha=?), (SELECT count(*) FROM commit_author_revalidations_pending)`, pulled).Scan(&revoked, &pending); err != nil {
		t.Fatal(err)
	}
	if revoked != 1 || pending != 0 {
		t.Fatalf("revoked=%d pending=%d after the first hook", revoked, pending)
	}
}
