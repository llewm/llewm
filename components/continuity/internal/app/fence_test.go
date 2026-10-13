package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func fenceApp(t *testing.T, required ...string) *App {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	policy, err := lease.ParsePolicy(required)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	return &App{
		Root: root, DB: db, ProjectID: "p", Config: config.Config{Project: config.Project{Name: "test"}},
		Checkpoints: checkpoints, TrackPlans: trackplan.Service{DB: db, Root: root}, Leases: lease.Service{DB: db}, LeasePolicy: policy,
		Bootstrap: bootstrap.Compiler{Checkpoints: checkpoints, MaxBytes: 12_000, GlobalRoot: t.TempDir()},
	}
}

// fenceCheckpoint continues taskID from its current tip, as a caller that has
// read the tip does.
func fenceCheckpoint(t *testing.T, application *App, taskID string, claim lease.Claim) checkpoint.Checkpoint {
	t.Helper()
	value := checkpoint.Checkpoint{TaskID: taskID, Task: "Task " + taskID, Summary: "Working", NextAction: "Continue", Fence: lease.Fence{Claim: claim}}
	if taskID != "" {
		tip, err := application.Checkpoints.LatestSelected(context.Background(), "p", checkpoint.Selection{TaskID: taskID})
		if err != nil {
			t.Fatal(err)
		}
		if tip != nil {
			value.PredecessorCheckpointID = tip.ID
		}
	}
	return value
}

func stampOf(t *testing.T, application *App, kind, id string) *lease.Stamp {
	t.Helper()
	stamp, err := lease.GetStamp(context.Background(), application.DB, "p", kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return stamp
}

func TestCheckpointFencing(t *testing.T) {
	ctx := context.Background()
	application := fenceApp(t, "task:locked")
	acquire := func(task, session string) lease.Lease {
		t.Helper()
		result, err := application.Leases.Acquire(ctx, lease.Request{ProjectID: "p", ScopeKind: "task", ScopeID: task, Holder: "agent", SessionID: session})
		if err != nil {
			t.Fatal(err)
		}
		return result.Lease
	}

	// Advisory scope: no lease is marked unleased and the write proceeds.
	created, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "free", lease.Claim{}))
	if err != nil || created.Lease == nil || created.Lease.State != lease.Unleased || created.Lease.Token != 0 {
		t.Fatalf("advisory unleased: %+v %v", created.Lease, err)
	}
	if got := stampOf(t, application, lease.StampCheckpoint, created.Checkpoint.ID); got == nil || got.State != lease.Unleased || got.Scope() != "task:free" {
		t.Fatalf("stored stamp: %+v", got)
	}

	// Every checkpoint names a task, so there is no unleasable taskless save.
	if _, err = application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "", lease.Claim{})); err == nil || !strings.Contains(err.Error(), "requires a task_id") {
		t.Fatalf("taskless checkpoint was accepted: %v", err)
	}

	// Advisory scope under its current lease records the token.
	held := acquire("free", "s1")
	created, err = application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "free", lease.Claim{Holder: "agent", SessionID: "s1", Token: held.Token}))
	if err != nil || created.Lease.State != lease.Leased || created.Lease.Token != held.Token || created.Lease.Note() != "" {
		t.Fatalf("advisory leased: %+v %v", created.Lease, err)
	}

	// A claim that no longer holds is marked lapsed on an advisory scope.
	created, err = application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "free", lease.Claim{Holder: "agent", SessionID: "s1", Token: held.Token + 5}))
	if err != nil || created.Lease.State != lease.Lapsed || created.Lease.Token != held.Token+5 || created.Lease.Note() == "" {
		t.Fatalf("advisory lapsed: %+v %v", created.Lease, err)
	}

	// Required scope: refused without a lease, with another holder's token, or with a stale one.
	before := countRows(t, application, "checkpoints")
	if _, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "locked", lease.Claim{})); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("required without lease: %v", err)
	}
	other := acquire("locked", "s2")
	if _, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "locked", lease.Claim{Holder: "agent", SessionID: "s1", Token: other.Token})); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("required with another holder's token: %v", err)
	}
	if got := countRows(t, application, "checkpoints"); got != before || countRows(t, application, "lease_stamps") != 3 {
		t.Fatalf("refused writes left rows: checkpoints %d (was %d), stamps %d", got, before, countRows(t, application, "lease_stamps"))
	}
	created, err = application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "locked", lease.Claim{Holder: "agent", SessionID: "s2", Token: other.Token}))
	if err != nil || created.Lease.State != lease.Leased {
		t.Fatalf("required with lease: %+v %v", created.Lease, err)
	}

	// Releasing the lease makes the same claim stale.
	if _, err := application.Leases.Release(ctx, lease.Request{ProjectID: "p", ScopeKind: "task", ScopeID: "locked", Holder: "agent", SessionID: "s2", Token: other.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "locked", lease.Claim{Holder: "agent", SessionID: "s2", Token: other.Token})); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("required after release: %v", err)
	}
	// A claim without a token is a malformed claim, not an unleased write.
	if _, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "free", lease.Claim{Holder: "agent"})); err == nil {
		t.Fatal("claim without token accepted")
	}
}

func TestWildcardPolicyRequiresEveryTask(t *testing.T) {
	application := fenceApp(t, "task:*")
	if _, err := application.SaveCheckpoint(context.Background(), fenceCheckpoint(t, application, "anything", lease.Claim{})); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("wildcard policy let an unleased write through: %v", err)
	}
}

func countRows(t *testing.T, application *App, table string) int {
	t.Helper()
	var count int
	if err := application.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPlanFencing(t *testing.T) {
	ctx := context.Background()
	application := fenceApp(t, "track_plan:locked")
	plan := func(id string, claim lease.Claim) trackplan.Plan {
		return trackplan.Plan{ProjectID: "p", ID: id, RoadmapNodeID: "node", Title: "Plan", Summary: "Summary", Status: "proposed", Creator: "owner",
			Items: []trackplan.Item{{ID: "a", Position: 1, Title: "Item", Detail: "Detail", Status: "pending"}}, Fence: lease.Fence{Claim: claim}}
	}

	created, err := application.AppendPlan(ctx, plan("free", lease.Claim{}))
	if err != nil || created.Lease == nil || created.Lease.State != lease.Unleased || created.Lease.RecordID != "free@1" {
		t.Fatalf("advisory plan: %+v %v", created.Lease, err)
	}
	if got := stampOf(t, application, lease.StampPlan, "free@1"); got == nil || got.Scope() != "track_plan:free" {
		t.Fatalf("stored plan stamp: %+v", got)
	}

	if _, err := application.AppendPlan(ctx, plan("locked", lease.Claim{})); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("required plan without lease: %v", err)
	}
	if got, err := application.TrackPlans.Plan(ctx, "p", "locked", 0); err != nil || got != nil {
		t.Fatalf("refused plan was stored: %+v %v", got, err)
	}
	held, err := application.Leases.Acquire(ctx, lease.Request{ProjectID: "p", ScopeKind: "track_plan", ScopeID: "locked", Holder: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	claim := lease.Claim{Holder: "agent", Token: held.Lease.Token}
	created, err = application.AppendPlan(ctx, plan("locked", claim))
	if err != nil || created.Lease.State != lease.Leased || created.Lease.Token != held.Lease.Token {
		t.Fatalf("required plan under lease: %+v %v", created.Lease, err)
	}

	// A review revision is fenced like any other revision of the plan.
	if _, err := application.AcceptPlan(ctx, "locked", "accepted", "owner", lease.Claim{}); !errors.Is(err, lease.ErrLeaseRequired) {
		t.Fatalf("required review without lease: %v", err)
	}
	reviewed, err := application.AcceptPlan(ctx, "locked", "accepted", "owner", claim)
	if err != nil || reviewed.Revision != 2 || reviewed.Lease.State != lease.Leased {
		t.Fatalf("review under lease: %+v %v", reviewed, err)
	}
}

func TestLeasePolicyRejectsBadPatterns(t *testing.T) {
	for _, pattern := range []string{"task", "file:x", "task:", "plan:*"} {
		if _, err := lease.ParsePolicy([]string{pattern}); err == nil {
			t.Fatalf("pattern %q accepted", pattern)
		}
	}
}

func TestStaleWriteIsListedAndResolved(t *testing.T) {
	ctx := context.Background()
	application := fenceApp(t)
	request := lease.Request{ProjectID: "p", ScopeKind: "task", ScopeID: "build", Holder: "agent", SessionID: "s1"}
	first, err := application.Leases.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Token = first.Lease.Token
	if _, err := application.Leases.Release(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Leases.Acquire(ctx, lease.Request{ProjectID: "p", ScopeKind: "task", ScopeID: "build", Holder: "agent", SessionID: "s2"}); err != nil {
		t.Fatal(err)
	}
	written, err := application.SaveCheckpoint(ctx, fenceCheckpoint(t, application, "build", lease.Claim{Holder: "agent", SessionID: "s1", Token: first.Lease.Token}))
	if err != nil {
		t.Fatal(err)
	}
	subject := lease.StaleSubject(lease.StampCheckpoint, written.Checkpoint.ID)
	writes, err := lease.StaleWrites(ctx, application.DB, "p", "")
	if err != nil || len(writes) != 1 || writes[0].Ref() != subject || writes[0].Newer.Token != 2 {
		t.Fatalf("stale writes: %+v %v", writes, err)
	}
	if _, err := application.ResolveStaleWrite(ctx, subject, "maybe", "owner", "user_reported"); err == nil {
		t.Fatal("an unknown accept value was taken")
	}
	resolved, err := application.ResolveStaleWrite(ctx, subject, "", "owner", "user_reported")
	if err != nil || resolved.Accepted != lease.AcceptLease {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	if writes, err := lease.StaleWrites(ctx, application.DB, "p", ""); err != nil || len(writes) != 0 {
		t.Fatalf("after resolve: %+v %v", writes, err)
	}
}
