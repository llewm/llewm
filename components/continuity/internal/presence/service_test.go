package presence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

func testService(t *testing.T) (context.Context, Service, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "presence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return ctx, Service{DB: db}, "p"
}

func TestPresenceLifecycleIsAppendOnlyAndExpires(t *testing.T) {
	ctx, service, projectID := testService(t)
	start := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	input := Observation{ProjectID: projectID, Client: "codex", SessionID: "s", State: "active", TaskID: "task", CheckoutPath: "/repo", WorktreePath: "/repo", Branch: "main", At: start, TTL: time.Minute}
	first, inserted, err := service.Observe(ctx, input)
	if err != nil || !inserted || first.Sequence != 1 || first.ClaimID != ClaimID(projectID, "codex", "s") {
		t.Fatalf("first observation: %+v inserted=%t err=%v", first, inserted, err)
	}
	input.At = start.Add(10 * time.Second)
	if same, inserted, err := service.Observe(ctx, input); err != nil || inserted || same.ID != first.ID {
		t.Fatalf("coalesced refresh: %+v inserted=%t err=%v", same, inserted, err)
	}
	input.At = start.Add(40 * time.Second)
	second, inserted, err := service.Observe(ctx, input)
	if err != nil || !inserted || second.Sequence != 2 {
		t.Fatalf("refresh: %+v inserted=%t err=%v", second, inserted, err)
	}
	report, err := service.List(ctx, projectID, Query{Now: start.Add(2 * time.Minute)})
	if err != nil || len(report.Presence) != 1 || report.Presence[0].DerivedState != "expired" {
		t.Fatalf("derived expiry: %+v err=%v", report, err)
	}
	if count, err := service.Expire(ctx, projectID, start.Add(2*time.Minute)); err != nil || count != 1 {
		t.Fatalf("expire: count=%d err=%v", count, err)
	}
	if live, err := service.List(ctx, projectID, Query{SessionID: "s", LiveOnly: true, Limit: 10, Now: start.Add(2 * time.Minute)}); err != nil || len(live.Presence) != 0 {
		t.Fatalf("terminal claim exposed an older active transition: %+v err=%v", live, err)
	}
	history, err := service.List(ctx, projectID, Query{SessionID: "s", History: true, Limit: 10, Now: start.Add(2 * time.Minute)})
	if err != nil || len(history.Presence) != 3 || history.Presence[0].State != "expired" {
		t.Fatalf("history: %+v err=%v", history, err)
	}
	if _, err := service.DB.ExecContext(ctx, `UPDATE presence_events SET state='cancelled' WHERE id=?`, first.ID); err == nil {
		t.Fatal("append-only trigger allowed update")
	}
}

func TestPresenceFiltersExactIdentityAndLiveState(t *testing.T) {
	ctx, service, projectID := testService(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, input := range []Observation{
		{ProjectID: projectID, Client: "codex", SessionID: "a", State: "active", TaskID: "one", CheckoutPath: "/repo", WorktreePath: "/repo", Branch: "main", At: now},
		{ProjectID: projectID, Client: "claude-code", SessionID: "b", State: "yielded", TaskID: "two", CheckoutPath: "/repo", WorktreePath: "/repo-wt", Branch: "feature", At: now},
	} {
		if _, _, err := service.Observe(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	report, err := service.List(ctx, projectID, Query{TaskID: "one", Branch: "main", LiveOnly: true, Now: now, FocusTask: "one", FocusWorktree: "/repo"})
	if err != nil || len(report.Presence) != 1 || report.Presence[0].SessionID != "a" || len(report.Presence[0].Overlap) != 2 {
		t.Fatalf("filtered presence: %+v err=%v", report, err)
	}
}

func TestPresenceAppliesDerivedStateBeforeLimit(t *testing.T) {
	ctx, service, projectID := testService(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if _, _, err := service.Observe(ctx, Observation{ProjectID: projectID, Client: "codex", SessionID: "live", State: "active", At: now, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := service.Observe(ctx, Observation{ProjectID: projectID, Client: "codex", SessionID: fmt.Sprintf("yielded-%d", i), State: "yielded", At: now.Add(time.Duration(i+1) * time.Second), TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
	}
	report, err := service.List(ctx, projectID, Query{LiveOnly: true, Limit: 1, Now: now.Add(10 * time.Second)})
	if err != nil || len(report.Presence) != 1 || report.Presence[0].SessionID != "live" {
		t.Fatalf("bounded live query: %+v err=%v", report, err)
	}
	expired, err := service.List(ctx, projectID, Query{State: "expired", Limit: 10, Now: now.Add(2 * time.Hour)})
	if err != nil || len(expired.Presence) != 6 {
		t.Fatalf("derived expired query: %+v err=%v", expired, err)
	}
}

func TestCleanupCandidatesRequireCleanIntegratedInactiveWorktree(t *testing.T) {
	ctx, service, projectID := testService(t)
	root := filepath.Join(t.TempDir(), "repo")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		all := append([]string{"-C", dir}, args...)
		if output, err := gitenv.Command(ctx, all...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git(root, "init", "-b", "main")
	git(root, "config", "user.name", "Presence Test")
	git(root, "config", "user.email", "presence@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "file.txt")
	git(root, "commit", "-m", "initial")
	git(root, "worktree", "add", "-b", "feature", worktree)

	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	report, err := service.List(ctx, projectID, Query{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	values := CleanupCandidates(ctx, project.Repositories{Root: root}, report.Presence, now)
	if len(values) != 1 || !values[0].CleanupDue {
		t.Fatalf("inactive candidate: %+v", values)
	}
	if _, _, err := service.Observe(ctx, Observation{ProjectID: projectID, Client: "codex", SessionID: "live", State: "active", WorktreePath: worktree, At: now}); err != nil {
		t.Fatal(err)
	}
	report, err = service.List(ctx, projectID, Query{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	values = CleanupCandidates(ctx, project.Repositories{Root: root}, report.Presence, now)
	if len(values) != 1 || values[0].CleanupDue || len(values[0].LiveSessions) != 1 {
		t.Fatalf("live candidate: %+v", values)
	}
}
