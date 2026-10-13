package lease

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func setup(t *testing.T) (*sql.DB, string, *time.Time, Service) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return db, projectID, &clock, Service{DB: db, Now: func() time.Time { return clock }}
}

func TestAcquireRenewReleaseAndTokens(t *testing.T) {
	ctx := context.Background()
	_, projectID, clock, service := setup(t)
	agentA := Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "t1", Holder: "claude-code", SessionID: "a"}
	agentB := Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "t1", Holder: "claude-code", SessionID: "b", TTL: time.Minute}

	first, err := service.Acquire(ctx, agentA)
	if err != nil {
		t.Fatal(err)
	}
	if first.Renewed || first.Lease.Token != 1 || first.Lease.State != Held || !first.Lease.ExpiresAt.Equal(clock.Add(DefaultTTL)) {
		t.Fatalf("first grant: %+v", first)
	}
	if margin := first.Lease.ExpiresAt.Sub(first.Lease.ValidUntil); margin != DefaultTTL/10 {
		t.Fatalf("margin %s", margin)
	}

	// Another session of the same holder is someone else.
	var conflict *ConflictError
	if _, err := service.Acquire(ctx, agentB); !errors.As(err, &conflict) || conflict.Current.Token != 1 || conflict.Current.SessionID != "a" {
		t.Fatalf("expected conflict naming session a, got %v", err)
	}

	// Acquiring again while holding renews under the same token.
	*clock = clock.Add(time.Minute)
	again, err := service.Acquire(ctx, agentA)
	if err != nil || !again.Renewed || again.Lease.Token != 1 || again.Lease.Renewals != 1 || !again.Lease.ExpiresAt.Equal(clock.Add(DefaultTTL)) {
		t.Fatalf("re-acquire: %+v %v", again, err)
	}
	renew := agentA
	renew.Token = 1
	renewedResult, err := service.Renew(ctx, renew)
	renewed := renewedResult.Lease
	if err != nil || renewed.Renewals != 2 {
		t.Fatalf("renew: %+v %v", renewed, err)
	}

	// A lapsed lease frees the scope; the old holder cannot renew it, and the
	// next grant gets a higher token.
	*clock = clock.Add(DefaultTTL)
	if _, err := service.Renew(ctx, renew); !errors.Is(err, ErrNotHeld) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("renewing a lapsed lease: %v", err)
	}
	second, err := service.Acquire(ctx, agentB)
	if err != nil || second.Lease.Token != 2 {
		t.Fatalf("second grant: %+v %v", second, err)
	}
	// The old token can neither renew nor release the new grant.
	if _, err := service.Release(ctx, renew); !errors.Is(err, ErrNotHeld) || !strings.Contains(err.Error(), "token 2") {
		t.Fatalf("stale release: %v", err)
	}
	release := agentB
	release.Token = 2
	releasedResult, err := service.Release(ctx, release)
	released := releasedResult.Lease
	if err != nil || released.State != Released || released.ReleasedAt == nil {
		t.Fatalf("release: %+v %v", released, err)
	}
	if _, err := service.Release(ctx, release); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("double release: %v", err)
	}
	third, err := service.Acquire(ctx, agentA)
	if err != nil || third.Lease.Token != 3 || third.Renewed {
		t.Fatalf("third grant: %+v %v", third, err)
	}

	history, err := service.History(ctx, projectID, "task", "t1", 0)
	if err != nil || len(history) != 3 {
		t.Fatalf("history: %+v %v", history, err)
	}
	if history[0].Token != 3 || history[0].State != Held || history[1].State != Released || history[1].Renewals != 0 || history[2].State != Expired || history[2].Renewals != 2 {
		t.Fatalf("history states: %+v", history)
	}
}

func TestReleaseOfALapsedLeaseAndListing(t *testing.T) {
	ctx := context.Background()
	_, projectID, clock, service := setup(t)
	for _, scope := range []string{"task:t1", "roadmap_node:n1", "track_plan:p1"} {
		kind, id, err := ParseScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Acquire(ctx, Request{ProjectID: projectID, ScopeKind: kind, ScopeID: id, Holder: "codex", TTL: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ParseScope("file:main.go"); err == nil {
		t.Fatal("file scopes are not supported")
	}
	if _, err := service.Acquire(ctx, Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "t2", Holder: "codex", TTL: time.Second}); err == nil {
		t.Fatal("TTL below the minimum was accepted")
	}
	if _, err := service.Acquire(ctx, Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "t2", Holder: "token=abcdef0123456789abcdef"}); err == nil {
		t.Fatal("a holder needing redaction was accepted")
	}
	held, err := service.List(ctx, projectID, false)
	if err != nil || len(held) != 3 {
		t.Fatalf("held: %+v %v", held, err)
	}
	*clock = clock.Add(2 * time.Minute)
	if held, err = service.List(ctx, projectID, false); err != nil || len(held) != 0 {
		t.Fatalf("held after expiry: %+v %v", held, err)
	}
	all, err := service.List(ctx, projectID, true)
	if err != nil || len(all) != 3 || all[0].State != Expired {
		t.Fatalf("all: %+v %v", all, err)
	}
	releasedResult, err := service.Release(ctx, Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "t1", Holder: "codex", Token: 1})
	released := releasedResult.Lease
	if err != nil || released.State != Released {
		t.Fatalf("releasing a lapsed lease: %+v %v", released, err)
	}
}

func TestConcurrentAcquiresGrantOneHolder(t *testing.T) {
	ctx := context.Background()
	db, projectID, _, _ := setup(t)
	service := Service{DB: db}
	const agents = 8
	var wait sync.WaitGroup
	results := make([]error, agents)
	for index := range agents {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, results[index] = service.Acquire(ctx, Request{ProjectID: projectID, ScopeKind: "task", ScopeID: "shared", Holder: "agent", SessionID: string(rune('a' + index))})
		}()
	}
	wait.Wait()
	granted := 0
	for _, err := range results {
		var conflict *ConflictError
		switch {
		case err == nil:
			granted++
		case errors.As(err, &conflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if granted != 1 {
		t.Fatalf("%d agents were granted the same scope", granted)
	}
}

func TestNotice(t *testing.T) {
	ctx := context.Background()
	db, projectID, clock, service := setup(t)
	if notice, err := Notice(ctx, db, projectID, "t1", []string{"n1"}, *clock); err != nil || notice != "" {
		t.Fatalf("empty notice: %q %v", notice, err)
	}
	for _, request := range []Request{
		{ScopeKind: "roadmap_node", ScopeID: "n1", Holder: "codex"},
		{ScopeKind: "task", ScopeID: "t1", Holder: "claude-code", SessionID: "s1"},
		{ScopeKind: "task", ScopeID: "other", Holder: "codex"},
	} {
		request.ProjectID = projectID
		if _, err := service.Acquire(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	notice, err := Notice(ctx, db, projectID, "t1", []string{"n1"}, *clock)
	if err != nil {
		t.Fatal(err)
	}
	want := "task:t1 is leased by claude-code (session s1) (token 1, expires 12:15Z); roadmap_node:n1 is leased by codex (token 1, expires 12:15Z). 1 other held lease (llewm lease list)."
	if !strings.HasPrefix(notice, want) {
		t.Fatalf("notice:\n%s\nwant prefix:\n%s", notice, want)
	}
	if notice, err = Notice(ctx, db, projectID, "", nil, *clock); err != nil || !strings.HasPrefix(notice, "3 other held leases") {
		t.Fatalf("notice without a task: %q %v", notice, err)
	}
}
