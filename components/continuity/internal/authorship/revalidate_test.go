package authorship

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestRevalidateRevokesAttributionsWithoutTimelyCreationEvidence(t *testing.T) {
	ctx := context.Background()
	upstream, root := t.TempDir(), t.TempDir()
	git(t, upstream, "init", "-q", "-b", "main")
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "base")
	git(t, root, "clone", "-q", upstream, ".")
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	unix := func(hash string) time.Time {
		t.Helper()
		seconds, err := strconv.ParseInt(git(t, root, "log", "-1", "--format=%ct", hash), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return time.Unix(seconds, 0).UTC()
	}
	attribute := func(hash, session string, at time.Time) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO commit_authors(project_id,member,commit_sha,session_id,client,task_id,tool_name,certainty,observed_at) VALUES ('p','',?,?,'codex','task-'||?,'Bash','observed',?)`,
			hash, session, session, at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	// Genuine: created here and observed right after the tool call.
	git(t, root, "commit", "-q", "--allow-empty", "-m", "local")
	local := git(t, root, "rev-parse", "HEAD")
	attribute(local, "maker", unix(local).Add(3*time.Second))

	// Genuine, in another worktree: its HEAD reflog is read from the main root.
	worktree := filepath.Join(t.TempDir(), "wt")
	git(t, root, "worktree", "add", "-q", "-b", "side", worktree)
	git(t, worktree, "commit", "-q", "--allow-empty", "-m", "in worktree")
	side := git(t, worktree, "rev-parse", "HEAD")
	attribute(side, "maker", unix(side).Add(time.Minute))

	// Pre-fix false attribution: incoming history credited to whoever pulled.
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "incoming")
	incoming := git(t, upstream, "rev-parse", "HEAD")
	git(t, root, "pull", "-q", "--no-rebase", "origin", "main")
	attribute(incoming, "puller", time.Now().UTC())

	// Created here, but observed long after by a later session.
	git(t, root, "commit", "-q", "--allow-empty", "-m", "late")
	late := git(t, root, "rev-parse", "HEAD")
	attribute(late, "puller", unix(late).Add(2*time.Hour))

	preview, err := Revalidate(ctx, db, "p", "", root, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	for _, item := range preview {
		keep[item.Commit] = item.Keep
	}
	if len(preview) != 4 || !keep[local] || !keep[side] || keep[incoming] || keep[late] {
		t.Fatalf("preview = %#v", preview)
	}
	if authors, _ := Authors(ctx, db, "p", "", []string{incoming}); len(authors) != 1 {
		t.Fatal("preview revoked an attribution")
	}

	if _, err := Revalidate(ctx, db, "p", "", root, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	authors, err := Authors(ctx, db, "p", "", []string{local, side, incoming, late})
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 || authors[local].SessionID != "maker" || authors[side].SessionID != "maker" {
		t.Fatalf("authors after apply = %#v", authors)
	}
	if commits, err := TaskCommits(ctx, db, "p", "task-puller"); err != nil || len(commits) != 0 {
		t.Fatalf("revoked task commits = %#v %v", commits, err)
	}
	var observations int
	if err := db.QueryRow(`SELECT count(*) FROM commit_authors`).Scan(&observations); err != nil || observations != 4 {
		t.Fatalf("observations = %d %v; revocation must not rewrite them", observations, err)
	}
	again, err := Revalidate(ctx, db, "p", "", root, true, time.Now())
	if err != nil || len(again) != 2 {
		t.Fatalf("second pass = %#v %v; want only the kept attributions", again, err)
	}
}
