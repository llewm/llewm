package memsync

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/lease"
)

func TestCrossMachineLeasesThroughGitRefs(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	laptop, box := newMachine(t, "laptop", remote), newMachine(t, "box", remote)
	laptopRefs, boxRefs := laptop.syncer.LeaseRemote(), box.syncer.LeaseRemote()
	laptopLeases := lease.Service{DB: laptop.db, Remote: laptopRefs}
	boxLeases := lease.Service{DB: box.db, Remote: boxRefs}
	request := func(m machine, holder string) lease.Request {
		return lease.Request{ProjectID: m.project, ScopeKind: "task", ScopeID: "build/ci", Holder: holder, TTL: time.Minute}
	}

	first, err := laptopLeases.Acquire(ctx, request(laptop, "claude-code"))
	if err != nil || first.Lease.Token != 1 || !first.Lease.OnRemote {
		t.Fatalf("laptop acquire: %+v %v", first, err)
	}
	var conflict *lease.ConflictError
	if _, err := boxLeases.Acquire(ctx, request(box, "codex")); !errors.As(err, &conflict) || conflict.Current.Token != 1 {
		t.Fatalf("box acquire: %v", err)
	}
	// A write against a version that is no longer the tip is refused by the
	// remote's compare-and-swap.
	scopes, err := boxRefs.Scopes(ctx)
	if err != nil || len(scopes["task:build/ci"].Events) != 1 {
		t.Fatalf("box scopes: %+v %v", scopes, err)
	}
	stale := scopes["task:build/ci"].Events[0]
	stale.Sequence, stale.Action, stale.RecordedAt = 2, "release", stale.RecordedAt.Add(time.Second)
	if _, err := boxRefs.Write(ctx, "", stale); !errors.Is(err, lease.ErrRemoteMoved) {
		t.Fatalf("write against an absent ref: %v", err)
	}
	released, err := laptopLeases.Release(ctx, lease.Request{ProjectID: laptop.project, ScopeKind: "task", ScopeID: "build/ci", Holder: "claude-code", Token: 1})
	if err != nil || released.Lease.State != lease.Released {
		t.Fatalf("laptop release: %+v %v", released, err)
	}
	taken, err := boxLeases.Acquire(ctx, request(box, "codex"))
	if err != nil || taken.Lease.Token != 2 {
		t.Fatalf("box acquire after release: %+v %v", taken, err)
	}
	refs, err := exec.Command("git", "--git-dir", remote, "for-each-ref", "--format=%(refname)", "refs/continuity/leases/").Output()
	if err != nil || strings.Count(strings.TrimSpace(string(refs)), "\n") != 0 || !strings.HasPrefix(string(refs), "refs/continuity/leases/5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a/task/") {
		t.Fatalf("remote lease refs: %q %v", refs, err)
	}

	// A sync with leases enabled mirrors the remote into the laptop's tables.
	laptop.syncer.Leases = &lease.Service{DB: laptop.db}
	result := laptop.sync(t, OutcomeSynced)
	if len(result.LeaseNotes) != 0 {
		t.Fatalf("lease notes: %v", result.LeaseNotes)
	}
	held, err := laptopLeases.List(ctx, laptop.project, false)
	if err != nil || len(held) != 1 || held[0].Token != 2 || held[0].Holder != "codex" || !held[0].OnRemote {
		t.Fatalf("laptop mirror after sync: %+v %v", held, err)
	}
}
