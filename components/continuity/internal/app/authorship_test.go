package app

import (
	"context"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/project"
)

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
