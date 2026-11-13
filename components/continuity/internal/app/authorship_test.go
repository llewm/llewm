package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/lease"
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

func TestRepairRetasksCommitsCreditedToFinishedTasks(t *testing.T) {
	ctx := context.Background()
	a := fenceApp(t)
	a.Presence = presence.Service{DB: a.DB}
	save := func(session, task, status string) time.Time {
		t.Helper()
		value := fenceCheckpoint(t, a, task, lease.Claim{})
		value.SessionID, value.Status = session, status
		var reconciliation checkpoint.ActionReconciliation
		if status == "completed" {
			open, err := a.Checkpoints.ListActions(ctx, "p", task, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range open {
				reconciliation.Updates = append(reconciliation.Updates, checkpoint.ActionUpdate{ID: action.ID, Status: "completed"})
			}
		}
		created, err := a.SaveCheckpointWithReconciliation(ctx, value, reconciliation)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		return created.Checkpoint.CreatedAt
	}
	attribute := func(commit, session, task string, at time.Time) {
		t.Helper()
		if _, err := a.DB.Exec(`INSERT INTO commit_authors(project_id,member,commit_sha,session_id,client,task_id,tool_name,certainty,observed_at) VALUES ('p','',?,?,'claude-code',NULLIF(?,''),'Bash','observed',?)`,
			commit, session, task, at.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// moved: finished A, committed new work, then checkpointed C.
	attribute("moved", "s1", "a", save("s1", "a", "completed").Add(time.Millisecond))
	save("s1", "c", "")
	// orphan: finished D and never checkpointed again.
	attribute("orphan", "s2", "d", save("s2", "d", "completed").Add(time.Millisecond))
	// reopened: finished E, committed, then reopened E.
	attribute("reopened", "s3", "e", save("s3", "e", "completed").Add(time.Millisecond))
	save("s3", "e", "")
	// working: B was in progress when the commit was observed, finished later.
	attribute("working", "s4", "b", save("s4", "b", "").Add(time.Millisecond))
	save("s4", "b", "completed")
	// untasked: an older build recorded no task before the session's first
	// checkpoint; inserted after it, so only the repair can retask it.
	attribute("untasked", "s5", "", save("s5", "f", "").Add(-time.Millisecond))
	if _, err := a.DB.Exec(`INSERT INTO commit_author_repairs_pending(project_id,repair,cutoff) VALUES ('p','retask',?)`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	a.repairAuthorship(ctx)
	commits := []string{"moved", "orphan", "reopened", "working", "untasked"}
	authors, err := authorship.Authors(ctx, a.DB, "p", "", commits)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]authorship.Author{
		"moved":    {SessionID: "s1", Client: "claude-code", TaskID: "c", TaskInferred: true},
		"orphan":   {SessionID: "s2", Client: "claude-code", TaskInferred: true},
		"reopened": {SessionID: "s3", Client: "claude-code", TaskID: "e"},
		"working":  {SessionID: "s4", Client: "claude-code", TaskID: "b"},
		"untasked": {SessionID: "s5", Client: "claude-code", TaskID: "f", TaskInferred: true},
	}
	for _, commit := range commits {
		if authors[commit] != want[commit] {
			t.Errorf("%s = %+v, want %+v", commit, authors[commit], want[commit])
		}
	}
	var observations, amendments, pending int
	if err := a.DB.QueryRow(`SELECT (SELECT count(*) FROM commit_authors WHERE task_id IN ('a','d','e','b') OR task_id IS NULL), (SELECT count(*) FROM commit_author_amendments), (SELECT count(*) FROM commit_author_repairs_pending)`).Scan(&observations, &amendments, &pending); err != nil {
		t.Fatal(err)
	}
	if observations != 5 || amendments != 3 || pending != 0 {
		t.Fatalf("observations=%d amendments=%d pending=%d", observations, amendments, pending)
	}
	a.repairAuthorship(ctx)
	if err := a.DB.QueryRow(`SELECT count(*) FROM commit_author_amendments`).Scan(&amendments); err != nil || amendments != 3 {
		t.Fatalf("second repair appended amendments: %d %v", amendments, err)
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
		INSERT INTO commit_author_repairs_pending(project_id,repair,cutoff) VALUES ('p','revalidate','2026-11-13T00:00:00Z')`, pulled); err != nil {
		t.Fatal(err)
	}
	a.ObserveHeads(ctx, "codex", "s", "sessionstart", "", nil)
	var revoked, pending int
	if err := a.DB.QueryRow(`SELECT (SELECT count(*) FROM commit_author_amendments WHERE kind='revoke' AND commit_sha=?), (SELECT count(*) FROM commit_author_repairs_pending)`, pulled).Scan(&revoked, &pending); err != nil {
		t.Fatal(err)
	}
	if revoked != 1 || pending != 0 {
		t.Fatalf("revoked=%d pending=%d after the first hook", revoked, pending)
	}
}
