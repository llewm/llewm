package memsync

import (
	"context"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/fork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func (m machine) supersedeDecision(t *testing.T, id, old string) error {
	t.Helper()
	_, err := (decision.Service{DB: m.db}).Append(context.Background(), decision.Record{ProjectID: m.project, ID: id, Title: id, Rationale: "Because " + id, Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted", SupersedesID: old})
	return err
}

func (m machine) narrate(t *testing.T, id, old string) {
	t.Helper()
	if _, err := (worknarrative.Service{DB: m.db}).Append(context.Background(), worknarrative.Record{ID: id, ProjectID: m.project, Title: id, Trigger: "manual", Creator: "owner", SupersedesID: old, Claims: []worknarrative.Claim{{Kind: "decision", Text: "Narrated " + id, Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
}

func (m machine) supersessionForks(t *testing.T) []fork.SupersessionFork {
	t.Helper()
	forks, err := fork.SupersessionForks(context.Background(), m.db, m.project, "")
	if err != nil {
		t.Fatal(err)
	}
	return forks
}

func (m machine) supersededBy(t *testing.T, id string) string {
	t.Helper()
	value, err := (decision.Service{DB: m.db}).Show(context.Background(), m.project, id, 65536)
	if err != nil {
		t.Fatal(err)
	}
	return value.SupersededBy
}

func (m machine) narrativeState(t *testing.T, id string) string {
	t.Helper()
	shown, err := (worknarrative.Service{DB: m.db}).Show(context.Background(), m.project, id, 4096)
	if err != nil || shown == nil {
		t.Fatalf("show narrative %s: %#v %v", id, shown, err)
	}
	return shown.Record.State
}

// Before supersession forks, the second machine's whole sync failed on the
// one-successor constraint, and kept failing, so it never imported or pushed
// again.
func TestConcurrentSupersessionsBecomeAForkInsteadOfAFailedSync(t *testing.T) {
	remote := bareRemote(t)
	a := newMachine(t, "a", remote)
	b := newMachine(t, "b", remote)
	a.decide(t, "base", "Base")
	a.narrate(t, "story", "")
	a.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	// Both machines supersede the same decision and narrative before syncing.
	if err := a.supersedeDecision(t, "from-a", "base"); err != nil {
		t.Fatal(err)
	}
	a.narrate(t, "story-a", "story")
	a.sync(t, OutcomeSynced)
	if err := b.supersedeDecision(t, "from-b", "base"); err != nil {
		t.Fatal(err)
	}
	b.narrate(t, "story-b", "story")
	forked := b.sync(t, OutcomeSynced)
	if forked.SupersessionForks != 2 || forked.Collisions != 0 || forked.ImportedHead == "" || forked.UnpushedCommits != 0 || !strings.Contains(forked.Detail, "2 records were superseded more than once") {
		t.Fatalf("fork sync: %#v", forked)
	}
	if again := b.sync(t, OutcomeSynced); again.SupersessionForks != 0 {
		t.Fatalf("repeat sync recounted forks: %#v", again)
	}
	if seen := a.sync(t, OutcomeSynced); seen.SupersessionForks != 2 {
		t.Fatalf("machine A: %#v", seen)
	}
	for name, m := range map[string]machine{"A": a, "B": b} {
		forks := m.supersessionForks(t)
		if len(forks) != 2 || forks[0].Ref() != "decision:base" || forks[1].Ref() != "narrative:story" || len(forks[0].Heads) != 2 || forks[0].Proposed().ID != "from-b" || forks[0].Heads[1].ID != "from-a" || forks[1].Proposed().ID != "story-b" {
			t.Fatalf("machine %s forks: %#v", name, forks)
		}
		// Both successors read as current until the fork is resolved.
		if m.narrativeState(t, "story-a") != "current" || m.narrativeState(t, "story-b") != "current" || m.narrativeState(t, "story") != "superseded" {
			t.Fatalf("machine %s narrative states before resolving", name)
		}
	}
	// A superseded record still cannot be superseded again locally.
	if err := a.supersedeDecision(t, "third", "base"); err == nil || !strings.Contains(err.Error(), "already superseded") {
		t.Fatalf("a third local successor: %v", err)
	}

	// Machine B accepts machine A's decision and its own narrative.
	resolver := &app.App{DB: b.db, ProjectID: b.project}
	if _, err := resolver.ResolveSupersessionFork(context.Background(), "decision:base", "base", "owner", "user_reported"); err == nil {
		t.Fatal("a record that is not a head was accepted")
	}
	resolved, err := resolver.ResolveSupersessionFork(context.Background(), "decision:base", "from-a", "owner", "user_reported")
	if err != nil || resolved.Family != fork.FamilySupersession || resolved.Subject != "decision:base" || resolved.AcceptedHead != "from-a" || len(resolved.RejectedHeads) != 1 || resolved.RejectedHeads[0] != "from-b" {
		t.Fatalf("resolve decision: %#v %v", resolved, err)
	}
	if _, err := resolver.ResolveSupersessionFork(context.Background(), "narrative:story", "", "owner", "user_reported"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveSupersessionFork(context.Background(), "decision:base", "", "owner", "user_reported"); err == nil {
		t.Fatal("a resolved fork was resolved again")
	}
	// A rejected head is no longer current, so it cannot be superseded locally.
	if err := b.supersedeDecision(t, "after-b", "from-b"); err == nil {
		t.Fatal("a rejected head was superseded")
	}
	b.sync(t, OutcomeSynced)
	a.sync(t, OutcomeSynced)
	for name, m := range map[string]machine{"A": a, "B": b} {
		if forks := m.supersessionForks(t); len(forks) != 0 {
			t.Fatalf("machine %s forks after resolving: %#v", name, forks)
		}
		if m.supersededBy(t, "base") != "from-a" || m.supersededBy(t, "from-b") != "from-a" || m.supersededBy(t, "from-a") != "" {
			t.Fatalf("machine %s superseded_by: base %q, from-b %q, from-a %q", name, m.supersededBy(t, "base"), m.supersededBy(t, "from-b"), m.supersededBy(t, "from-a"))
		}
		if m.narrativeState(t, "story-b") != "current" || m.narrativeState(t, "story-a") != "superseded" {
			t.Fatalf("machine %s narrative states after resolving", name)
		}
	}
	// The accepted head continues normally.
	if err := a.supersedeDecision(t, "next", "from-a"); err != nil {
		t.Fatal(err)
	}
}
