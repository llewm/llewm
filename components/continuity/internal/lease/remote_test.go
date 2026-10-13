package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

// fakeRemote is a shared memory remote: each scope is a chain of events and
// Write is a compare-and-swap on the chain's length.
type fakeRemote struct {
	mu      sync.Mutex
	scopes  map[string][]RemoteEvent
	offline bool
	// beforeWrite runs once before the next write, to simulate a racing writer.
	beforeWrite func()
}

func (f *fakeRemote) Scopes(context.Context) (map[string]RemoteScope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return nil, fmt.Errorf("%w: no route", ErrRemoteUnreachable)
	}
	result := map[string]RemoteScope{}
	for key, events := range f.scopes {
		newest := make([]RemoteEvent, len(events))
		for index, event := range events {
			newest[len(events)-1-index] = event
		}
		result[key] = RemoteScope{Version: newest[0].Version, Events: newest}
	}
	return result, nil
}

func (f *fakeRemote) Write(_ context.Context, expected string, event RemoteEvent) (string, error) {
	if hook := f.beforeWrite; hook != nil {
		f.beforeWrite = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return "", fmt.Errorf("%w: no route", ErrRemoteUnreachable)
	}
	if err := event.Validate(); err != nil {
		return "", err
	}
	events := f.scopes[event.Scope()]
	version := ""
	if len(events) > 0 {
		version = events[len(events)-1].Version
	}
	if version != expected {
		return "", ErrRemoteMoved
	}
	event.Version = fmt.Sprintf("v%d-%s", len(events)+1, event.Scope())
	f.scopes[event.Scope()] = append(events, event)
	return event.Version, nil
}

type machine struct {
	db        *sql.DB
	projectID string
	service   Service
}

func newMachine(t *testing.T, remote Remote, instance string, clock *time.Time) machine {
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
	if _, err := db.ExecContext(ctx, `INSERT INTO project_memory_identities(project_id,lineage_id,instance_id,association_source,authority,associated_at,schema_version) VALUES(?,?,?,?,?,?,1)`, projectID, "lineage:shared", instance, "generated", "caller_asserted", clock.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return machine{db: db, projectID: projectID, service: Service{DB: db, Now: func() time.Time { return *clock }, Remote: remote}}
}

func (m machine) request(holder string) Request {
	return Request{ProjectID: m.projectID, ScopeKind: "task", ScopeID: "t1", Holder: holder, TTL: time.Minute}
}

func TestCrossMachineLeasesShareTokensAndHonorTheTakeoverGrace(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{scopes: map[string][]RemoteEvent{}}
	laptop, box := newMachine(t, remote, "instance:laptop", &clock), newMachine(t, remote, "instance:box", &clock)

	first, err := laptop.service.Acquire(ctx, laptop.request("claude-code"))
	if err != nil || first.Lease.Token != 1 || !first.Lease.OnRemote || first.Warning != "" {
		t.Fatalf("laptop acquire: %+v %v", first, err)
	}
	// The same holder name on another machine is someone else.
	var conflict *ConflictError
	if _, err := box.service.Acquire(ctx, box.request("claude-code")); !errors.As(err, &conflict) || conflict.Current.InstanceID != "instance:laptop" || !strings.Contains(err.Error(), "claude-code on instance:laptop") {
		t.Fatalf("box acquire while held: %v", err)
	}
	// The box mirrored the laptop's grant, so its own fence sees it.
	if leases, err := box.service.List(ctx, box.projectID, false); err != nil || len(leases) != 1 || leases[0].InstanceID != "instance:laptop" {
		t.Fatalf("box mirror: %+v %v", leases, err)
	}
	renewed, err := laptop.service.Renew(ctx, Request{ProjectID: laptop.projectID, ScopeKind: "task", ScopeID: "t1", Holder: "claude-code", Token: 1, TTL: time.Minute})
	if err != nil || renewed.Lease.Renewals != 1 || !renewed.Lease.OnRemote {
		t.Fatalf("laptop renew: %+v %v", renewed, err)
	}

	// Expired, but within the takeover grace: still the laptop's.
	clock = clock.Add(time.Minute + 10*time.Second)
	if _, err := box.service.Acquire(ctx, box.request("codex")); !errors.As(err, &conflict) || !strings.Contains(err.Error(), "takeover grace") {
		t.Fatalf("box acquire within the grace: %v", err)
	}
	clock = clock.Add(DefaultSkew)
	taken, err := box.service.Acquire(ctx, box.request("codex"))
	if err != nil || taken.Lease.Token != 2 || taken.Lease.InstanceID != "instance:box" {
		t.Fatalf("box acquire after the grace: %+v %v", taken, err)
	}
	// The laptop's lapsed token no longer renews, and it learns of token 2.
	if _, err := laptop.service.Renew(ctx, Request{ProjectID: laptop.projectID, ScopeKind: "task", ScopeID: "t1", Holder: "claude-code", Token: 1, TTL: time.Minute}); !errors.Is(err, ErrNotHeld) || !strings.Contains(err.Error(), "token 2") {
		t.Fatalf("laptop renew after takeover: %v", err)
	}
	released, err := box.service.Release(ctx, Request{ProjectID: box.projectID, ScopeKind: "task", ScopeID: "t1", Holder: "codex", Token: 2})
	if err != nil || released.Lease.State != Released {
		t.Fatalf("box release: %+v %v", released, err)
	}
	again, err := laptop.service.Acquire(ctx, laptop.request("claude-code"))
	if err != nil || again.Lease.Token != 3 {
		t.Fatalf("laptop acquire after release: %+v %v", again, err)
	}
}

func TestCrossMachineAcquireRetriesWhenTheRemoteMoves(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{scopes: map[string][]RemoteEvent{}}
	laptop, box := newMachine(t, remote, "instance:laptop", &clock), newMachine(t, remote, "instance:box", &clock)
	// The box wins the race between the laptop's read and write.
	remote.beforeWrite = func() {
		if _, err := box.service.Acquire(ctx, box.request("codex")); err != nil {
			t.Errorf("racing acquire: %v", err)
		}
	}
	var conflict *ConflictError
	if _, err := laptop.service.Acquire(ctx, laptop.request("claude-code")); !errors.As(err, &conflict) || conflict.Current.InstanceID != "instance:box" {
		t.Fatalf("laptop acquire that lost the race: %v", err)
	}
}

func TestOfflineLeasesAreLocalOnlyUntilReconciled(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{scopes: map[string][]RemoteEvent{}, offline: true}
	laptop, box := newMachine(t, remote, "instance:laptop", &clock), newMachine(t, remote, "instance:box", &clock)

	policy, err := ParsePolicy([]string{"task:locked"})
	if err != nil {
		t.Fatal(err)
	}
	laptop.service.Policy = policy
	locked := laptop.request("claude-code")
	locked.ScopeID = "locked"
	if _, err := laptop.service.Acquire(ctx, locked); !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("required scope offline: %v", err)
	}
	local, err := laptop.service.Acquire(ctx, laptop.request("claude-code"))
	if err != nil || local.Lease.OnRemote || !strings.Contains(local.Warning, "local-only") {
		t.Fatalf("advisory scope offline: %+v %v", local, err)
	}

	remote.offline = false
	notes, err := laptop.service.Reconcile(ctx, laptop.projectID)
	if err != nil || len(notes) != 0 {
		t.Fatalf("reconcile: %v %v", notes, err)
	}
	if leases, _ := laptop.service.List(ctx, laptop.projectID, false); len(leases) != 1 || !leases[0].OnRemote {
		t.Fatalf("published lease: %+v", leases)
	}
	var conflict *ConflictError
	if _, err := box.service.Acquire(ctx, box.request("codex")); !errors.As(err, &conflict) {
		t.Fatalf("box sees the published lease: %v", err)
	}

	// A local-only grant the remote overtook stays local and is noted.
	remote.offline = true
	clock = clock.Add(2 * time.Minute)
	late, err := laptop.service.Acquire(ctx, Request{ProjectID: laptop.projectID, ScopeKind: "task", ScopeID: "t2", Holder: "claude-code", TTL: time.Minute})
	if err != nil || late.Lease.Token != 1 {
		t.Fatalf("second offline acquire: %+v %v", late, err)
	}
	remote.offline = false
	if _, err := box.service.Acquire(ctx, Request{ProjectID: box.projectID, ScopeKind: "task", ScopeID: "t2", Holder: "codex", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	notes, err = laptop.service.Reconcile(ctx, laptop.projectID)
	// Both machines granted token 1 while apart: the remote grant is not
	// mirrored over the local one, and the local one is not published.
	if err != nil || len(notes) != 2 || !strings.Contains(notes[0], "granted both here and on the remote") || !strings.Contains(notes[1], "local-only lease task:t2 token 1") {
		t.Fatalf("reconcile against an overtaken grant: %v %v", notes, err)
	}
}

func TestStaleWritesAreWritesAfterANewerGrant(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	remote := &fakeRemote{scopes: map[string][]RemoteEvent{}}
	laptop, box := newMachine(t, remote, "instance:laptop", &clock), newMachine(t, remote, "instance:box", &clock)
	if _, err := laptop.service.Acquire(ctx, laptop.request("claude-code")); err != nil {
		t.Fatal(err)
	}
	stamp := func(id string) Stamp {
		return Stamp{RecordKind: StampCheckpoint, RecordID: id, ScopeKind: "task", ScopeID: "t1", State: Leased, Token: 1, Holder: "claude-code", InstanceID: "instance:laptop", RecordedAt: clock}
	}
	// A write while the laptop held token 1 is fine.
	if err := InsertStamp(ctx, box.db, box.projectID, stamp("cp-early")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2*time.Minute + DefaultSkew)
	if _, err := box.service.Acquire(ctx, box.request("codex")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	// The laptop, offline, kept writing under token 1; its stamp syncs later.
	if err := InsertStamp(ctx, box.db, box.projectID, stamp("cp-late")); err != nil {
		t.Fatal(err)
	}
	writes, err := StaleWrites(ctx, box.db, box.projectID, "")
	if err != nil || len(writes) != 1 || writes[0].Ref() != "stale:checkpoint:cp-late" || writes[0].Newer.Token != 2 || writes[0].Newer.InstanceID != "instance:box" {
		t.Fatalf("stale writes: %+v %v", writes, err)
	}
	if notice := StaleNotice(writes); !strings.Contains(notice, "stale:checkpoint:cp-late was written under token 1 of task:t1 after token 2 was granted to codex") {
		t.Fatalf("notice: %s", notice)
	}
	if err := InsertStaleResolution(ctx, box.db, StaleResolution{ProjectID: box.projectID, ID: "r1", RecordKind: StampCheckpoint, RecordID: "cp-late", Accepted: AcceptLease, Resolver: "qj", Authority: "user_reported", RecordedAt: clock, SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if writes, err := StaleWrites(ctx, box.db, box.projectID, ""); err != nil || len(writes) != 0 {
		t.Fatalf("after resolution: %+v %v", writes, err)
	}
}
