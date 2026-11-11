package coordination

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func queueFixture(t *testing.T) *fixture {
	f := newFixture(t)
	job := func(os string, priority int) *JobSpec {
		return &JobSpec{OS: os, Arch: "amd64", Capabilities: []string{"native-tests"}, Priority: priority, Repository: "https://github.com/llewm/llewm", BaseRevision: "commit:baseline", AllowedChanges: []string{"temporary test fixtures only"}, ApprovalRequired: []string{"source publication"}}
	}
	f.must("dave", Command{Action: "propose", Definition: &Definition{Title: "Windows work", SourceRef: "continuity:project/plan:windows@1", Items: []Item{
		{ID: "first", Title: "First", AcceptanceCriteria: "Native evidence", Job: job("windows", 10)},
		{ID: "independent", Title: "Independent", AcceptanceCriteria: "Native evidence", Job: job("windows", 20)},
		{ID: "linux", Title: "Linux", AcceptanceCriteria: "Linux evidence", Job: job("linux", 0)},
		{ID: "dependent", Title: "Dependent", AcceptanceCriteria: "Review first", DependsOn: []string{"first"}, Job: job("windows", 0)},
		{ID: "blocked", Title: "Blocked", AcceptanceCriteria: "Clear blocker", BlockedReason: "Human credential input required", Job: job("windows", 0)},
		{ID: "manual", Title: "Manual only", AcceptanceCriteria: "No routing opt-in"},
	}}})
	f.must("dave", Command{Action: "accept-plan", Revision: 1})
	return f
}

func windowsProfile() *WorkerProfile {
	return &WorkerProfile{OS: "windows", Arch: "amd64", Capabilities: []string{"native-tests"}}
}
func queryQueue(t *testing.T, f *fixture, worker string) Queue {
	t.Helper()
	q, err := f.s.Queue(context.Background(), f.tokens[worker], QueueRequest{Worker: *windowsProfile(), PlanIDs: []string{"plan"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestQueueMatchesReviewedRequirementsAndIndependentWork(t *testing.T) {
	f := queueFixture(t)
	q := queryQueue(t, f, "alice")
	if len(q.Jobs) != 2 || q.Jobs[0].Item.ID != "first" || q.Jobs[1].Item.ID != "independent" || q.Jobs[0].SourceRef != "continuity:project/plan:windows@1" {
		t.Fatalf("wrong queue: %+v", q)
	}
	for _, profile := range []*WorkerProfile{nil, {OS: "linux", Arch: "amd64", Capabilities: []string{"native-tests"}}, {OS: "windows", Arch: "amd64"}} {
		if _, err := f.command("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: profile}); err == nil {
			t.Fatal("unmatched worker claimed routed job")
		}
	}
	claim := f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
	f.result("alice", claim.AttemptID, "native verification result")
	f.now = f.now.Add(AttemptTTL)
	q = queryQueue(t, f, "alice")
	if len(q.Jobs) != 1 || q.Jobs[0].Item.ID != "independent" {
		t.Fatalf("pending review blocked other work or requeued: %+v", q)
	}
	if _, err := f.command("jerry", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()}); err == nil {
		t.Fatal("pending review reclaimed")
	}
	f.must("alice", Command{Action: "claim", ItemID: "independent", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
}

func TestQueueLimitsAuthenticationAndAssignment(t *testing.T) {
	f := queueFixture(t)
	request := QueueRequest{Worker: *windowsProfile(), PlanIDs: []string{"plan"}, Limit: 1}
	q, err := f.s.Queue(context.Background(), f.tokens["alice"], request)
	if err != nil || len(q.Jobs) != 1 || !q.Truncated {
		t.Fatalf("limit: %+v %v", q, err)
	}
	if _, err := f.s.Queue(context.Background(), "invalid", request); !errors.Is(err, ErrForbidden) {
		t.Fatal("unauthenticated queue")
	}
	readonly, err := f.s.Enroll(context.Background(), "reader", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Queue(context.Background(), readonly, request); !errors.Is(err, ErrForbidden) {
		t.Fatal("read-only member received work queue")
	}
	for _, bad := range []QueueRequest{{Worker: *windowsProfile(), Limit: 1}, {Worker: *windowsProfile(), PlanIDs: []string{"plan", "plan"}, Limit: 1}, {Worker: *windowsProfile(), PlanIDs: []string{"plan"}, Limit: 129}, {Worker: WorkerProfile{OS: "windows", Arch: "amd64", Capabilities: []string{"tests", "tests"}}, PlanIDs: []string{"plan"}, Limit: 1}} {
		if _, err := f.s.Queue(context.Background(), f.tokens["alice"], bad); err == nil {
			t.Fatal("invalid queue query accepted")
		}
	}
	f.must("dave", Command{Action: "assign", Engineer: "alice", Reason: "Reviewed worker responsibility"})
	if q := queryQueue(t, f, "jerry"); len(q.Jobs) != 0 {
		t.Fatal("another engineer's jobs exposed as claimable")
	}
}

func TestReviewedResultRejectionPermitsNewAttempt(t *testing.T) {
	f := queueFixture(t)
	a := f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
	r := f.result("alice", a.AttemptID, "result awaiting review")
	f.review(r.SubmissionID, "reject")
	if q := queryQueue(t, f, "alice"); len(q.Jobs) != 2 {
		t.Fatalf("rejected work not ready again: %+v", q)
	}
	f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
}

func TestInvalidJobScopeRefused(t *testing.T) {
	for _, mutate := range []func(*JobSpec){func(j *JobSpec) { j.OS = "" }, func(j *JobSpec) { j.AllowedChanges = nil }, func(j *JobSpec) { j.Repository = "" }, func(j *JobSpec) { j.Priority = 101 }, func(j *JobSpec) { j.BaseRevision = "" }, func(j *JobSpec) { j.Capabilities = []string{"tests", "tests"} }} {
		j := JobSpec{OS: "windows", Arch: "amd64", Priority: 50, Repository: "repo", BaseRevision: "commit:1", AllowedChanges: []string{"tests"}}
		mutate(&j)
		if err := validateJob(j); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
}

func TestPausedJobDoesNotHoldUpIndependentWork(t *testing.T) {
	f := queueFixture(t)
	a := f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
	if _, err := f.command("jerry", Command{Action: "pause", AttemptID: a.AttemptID, Reason: "Cannot pause another worker"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("another member paused attempt")
	}
	f.must("alice", Command{Action: "pause", AttemptID: a.AttemptID, Reason: "Need local interactive credential prompt"})
	if q := queryQueue(t, f, "alice"); len(q.Jobs) != 1 || q.Jobs[0].Item.ID != "independent" {
		t.Fatalf("paused job rediscovered or blocked queue: %+v", q)
	}
	if _, err := f.command("alice", Command{Action: "unblock", ItemID: "first", Reason: "Self approve"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("worker unblocked its own job without assignment permission")
	}
	f.must("dave", Command{Action: "unblock", ItemID: "first", Reason: "Local authentication completed"})
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: a.AttemptID}); !errors.Is(err, ErrConflict) {
		t.Fatal("old paused attempt revived")
	}
	f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
}

func TestRoutedConcurrentClaimsHaveOneWinner(t *testing.T) {
	f := queueFixture(t)
	version := queryQueue(t, f, "alice").Jobs[0].Version
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, who := range []string{"alice", "jerry"} {
		wg.Add(1)
		go func(who string) {
			defer wg.Done()
			_, err := f.s.Execute(context.Background(), f.tokens[who], Command{SchemaVersion: 1, RequestID: "race-" + who, PlanID: "plan", Action: "claim", BaseVersion: version, ItemID: "first", Machine: who + "-pc", Agent: "claude", Worker: windowsProfile()})
			results <- err
		}(who)
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("claim winners=%d", won)
	}
}

func TestRejectingOldResultDoesNotReleaseNewWorker(t *testing.T) {
	f := queueFixture(t)
	a := f.must("alice", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
	r := f.result("alice", a.AttemptID, "old result")
	f.must("dave", Command{Action: "assign", Engineer: "jerry", Reason: "Explicit reviewed transfer"})
	b := f.must("jerry", Command{Action: "claim", ItemID: "first", Machine: "pc", Agent: "claude", Worker: windowsProfile()})
	f.review(r.SubmissionID, "reject")
	f.must("jerry", Command{Action: "resume", AttemptID: b.AttemptID})
}

func TestQueuePriorityTiesKeepSourceOrder(t *testing.T) {
	f := queueFixture(t)
	d := f.view().Plan.Revisions[0].Definition
	d.Items[0].ID = "z-first"
	d.Items[1].ID = "a-second"
	d.Items[1].Job.Priority = d.Items[0].Job.Priority
	d.Items[3].DependsOn = []string{"z-first"}
	f.must("dave", Command{Action: "propose", Definition: &d})
	f.must("dave", Command{Action: "accept-plan", Revision: 2})
	q := queryQueue(t, f, "alice")
	if len(q.Jobs) != 2 || q.Jobs[0].Item.ID != "z-first" || q.Jobs[1].Item.ID != "a-second" {
		t.Fatalf("source order lost: %+v", q)
	}
}
