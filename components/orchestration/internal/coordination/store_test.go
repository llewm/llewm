package coordination

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	t        *testing.T
	s        *Store
	tokens   map[string]string
	sequence int
	now      time.Time
	path     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "team.db")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, s: s, path: path, tokens: map[string]string{}, now: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
	s.now = func() time.Time { return f.now }
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"dave", "alice", "jerry"} {
		perms := []string{"plan", "work"}
		if id == "dave" {
			perms = []string{"plan", "accept", "assign", "review", "work"}
		}
		token, err := s.Enroll(context.Background(), id, perms)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[id] = token
	}
	return f
}

func (f *fixture) view() View {
	f.t.Helper()
	v, err := f.s.Read(context.Background(), f.tokens["dave"], "plan")
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}
func (f *fixture) command(actor string, c Command) (Receipt, error) {
	f.t.Helper()
	f.sequence++
	c.SchemaVersion = 1
	c.RequestID = fmt.Sprintf("request-%d", f.sequence)
	c.PlanID = "plan"
	if v, err := f.s.Read(context.Background(), f.tokens[actor], "plan"); err == nil {
		c.BaseVersion = v.Plan.Version
	}
	return f.s.Execute(context.Background(), f.tokens[actor], c)
}
func (f *fixture) must(actor string, c Command) Receipt {
	f.t.Helper()
	r, err := f.command(actor, c)
	if err != nil {
		f.t.Fatalf("%s %s: %v", actor, c.Action, err)
	}
	return r
}
func (f *fixture) plan() {
	items := []Item{}
	for i := 1; i <= 6; i++ {
		item := Item{ID: fmt.Sprintf("item-%d", i), Title: fmt.Sprintf("Work %d", i), AcceptanceCriteria: "tests pass and result integrates"}
		if i == 6 {
			item.DependsOn = []string{"item-4"}
		}
		items = append(items, item)
	}
	f.must("dave", Command{Action: "propose", Definition: &Definition{Title: "Shared plan", Context: "accepted context", Items: items}})
	f.must("dave", Command{Action: "accept-plan", Revision: 1})
}
func (f *fixture) claim(actor, item string) string {
	return f.must(actor, Command{Action: "claim", ItemID: item, Machine: actor + "-laptop", Agent: "codex"}).AttemptID
}
func (f *fixture) result(actor, attempt, context string) Receipt {
	return f.must(actor, Command{Action: "submit", AttemptID: attempt, Kind: "result", Context: context, ArtifactRefs: []string{"commit:example"}, VerificationRefs: []string{"test-run:example"}})
}
func (f *fixture) review(submission, decision string) {
	f.must("dave", Command{Action: "review", SubmissionID: submission, Decision: decision, Reason: "Reviewed against current criteria", VerificationRefs: []string{"integration-run:1"}})
}

func TestReassignmentPreservesCompletedContextAndLateWork(t *testing.T) {
	f := newFixture(t)
	f.plan()
	f.must("dave", Command{Action: "assign", Engineer: "alice", Reason: "Alice owns delivery"})
	for i := 1; i <= 3; i++ {
		attempt := f.claim("alice", fmt.Sprintf("item-%d", i))
		submission := f.result("alice", attempt, "Completed implementation")
		f.review(submission.SubmissionID, "accept")
	}
	// Another engineer's explicit child assignment survives a plan transfer.
	f.must("dave", Command{Action: "assign", ItemID: "item-5", Engineer: "dave", Reason: "Independent investigation"})
	daveAttempt := f.claim("dave", "item-5")
	aliceAttempt := f.claim("alice", "item-4")
	f.must("alice", Command{Action: "submit", AttemptID: aliceAttempt, Kind: "checkpoint", Context: "Found edge case; branch ref and tests attached", ArtifactRefs: []string{"branch:alice/item-4"}})
	before := f.view().Plan
	f.must("dave", Command{Action: "assign", Engineer: "jerry", Reason: "Alice unavailable; transfer remaining inherited work"})
	after := f.view().Plan
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("item-%d", i)
		if before.Work[id].CompletedBy != after.Work[id].CompletedBy || after.Work[id].CompletedBy == "" {
			t.Fatal("completed result lost")
		}
	}
	if len(after.Contributions) != 4 || !strings.Contains(after.Contributions[3].Context, "edge case") {
		t.Fatal("handoff context lost")
	}
	if after.Attempts[aliceAttempt].Engineer != "alice" {
		t.Fatal("attempt attribution rewritten")
	}
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: aliceAttempt}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "jerry") {
		t.Fatalf("resume failed to explain transfer: %v", err)
	}
	f.must("dave", Command{Action: "resume", AttemptID: daveAttempt})
	jerryAttempt := f.claim("jerry", "item-4")
	late := f.result("alice", aliceAttempt, "Offline contribution after reassignment")
	if late.Status != "needs_reconciliation" || !strings.Contains(late.Notice, "jerry") {
		t.Fatalf("late receipt: %+v", late)
	}
	if f.view().Plan.Work["item-4"].CompletedBy != "" {
		t.Fatal("late work silently completed item")
	}
	if _, err := f.command("dave", Command{Action: "review", SubmissionID: late.SubmissionID, Decision: "accept", Reason: "accept", VerificationRefs: []string{"tests"}}); err == nil {
		t.Fatal("stale result accepted without reconciliation")
	}
	f.review(late.SubmissionID, "adopt")
	v := f.view()
	if v.Plan.Work["item-4"].CompletedBy != late.SubmissionID {
		t.Fatal("adoption not recorded")
	}
	if _, err := f.command("jerry", Command{Action: "resume", AttemptID: jerryAttempt}); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded attempt resumed: %v", err)
	}
	// Jerry's subsequent offline work is retained and explicitly rejected as redundant.
	jerryLate := f.result("jerry", jerryAttempt, "Alternative implementation")
	if jerryLate.Status != "needs_reconciliation" {
		t.Fatal("completed item accepted more authority")
	}
	f.review(jerryLate.SubmissionID, "reject")
	if f.view().Plan.Work["item-4"].CompletedBy != late.SubmissionID {
		t.Fatal("rejection changed accepted result")
	}
	jerryView, err := f.s.Read(context.Background(), f.tokens["jerry"], "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(jerryView.Ready) != 1 || jerryView.Ready[0] != "item-6" {
		t.Fatalf("dependency readiness: %+v", jerryView.Ready)
	}
	// Persistence survives a second process opening the same coordinator database.
	s2, err := Open(f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	persisted, err := s2.Read(context.Background(), f.tokens["alice"], "plan")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Plan.Work["item-4"].CompletedBy != late.SubmissionID || len(persisted.Plan.Reviews) != 5 {
		t.Fatal("durable history incomplete")
	}
}

func TestConcurrentClaimsAndAssignmentCAS(t *testing.T) {
	f := newFixture(t)
	f.plan()
	version := f.view().Plan.Version
	second, err := Open(f.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.now = f.s.now
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, actor := range []string{"alice", "jerry"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			store := f.s
			if actor == "jerry" {
				store = second
			}
			_, err := store.Execute(context.Background(), f.tokens[actor], Command{SchemaVersion: 1, RequestID: actor + "-claim", Action: "claim", PlanID: "plan", BaseVersion: version, ItemID: "item-1", Machine: actor + "-pc", Agent: "agent"})
			out <- err
		}(actor)
	}
	wg.Wait()
	close(out)
	wins, conflicts := 0, 0
	for err := range out {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	_, err = f.s.Execute(context.Background(), f.tokens["dave"], Command{SchemaVersion: 1, RequestID: "stale-assign", Action: "assign", PlanID: "plan", BaseVersion: version, Engineer: "jerry", Reason: "stale view"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale assignment was not rejected: %v", err)
	}
}

func TestPermissionsAndCredentialRotation(t *testing.T) {
	f := newFixture(t)
	f.plan()
	for _, c := range []Command{{Action: "assign", Engineer: "alice", Reason: "unauthorized"}, {Action: "accept-plan", Revision: 1}, {Action: "review", SubmissionID: "missing", Decision: "adopt"}} {
		if _, err := f.command("alice", c); !errors.Is(err, ErrForbidden) {
			t.Fatalf("permission bypass: %v", err)
		}
	}
	a := f.claim("alice", "item-1")
	if _, err := f.command("jerry", Command{Action: "submit", AttemptID: a, Kind: "checkpoint", Context: "impersonation"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := f.command("dave", Command{Action: "assign", Engineer: "missing", Reason: "invalid"}); err == nil {
		t.Fatal("unknown assignee accepted")
	}
	old := f.tokens["alice"]
	newToken, err := f.s.Enroll(context.Background(), "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Read(context.Background(), old, "plan"); !errors.Is(err, ErrForbidden) {
		t.Fatal("old token still valid")
	}
	v, err := f.s.Read(context.Background(), newToken, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Ready) != 0 || v.Attempts[0].CanResume {
		t.Fatal("revoked work permission not reflected")
	}
	if err := f.s.Revoke(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Read(context.Background(), newToken, "plan"); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked token valid")
	}
}

func TestCollaborativeRevisionAndCompletedScope(t *testing.T) {
	f := newFixture(t)
	f.plan()
	a := f.claim("alice", "item-1")
	d := f.view().Plan.Revisions[0].Definition
	d.Items[1].Detail = "Alice refined implementation"
	f.must("alice", Command{Action: "propose", Definition: &d})
	f.must("alice", Command{Action: "resume", AttemptID: a}) // proposal alone has no authority
	d.Items[1].AcceptanceCriteria = "Jerry added a concrete integration test"
	f.must("jerry", Command{Action: "propose", Definition: &d})
	if _, err := f.command("dave", Command{Action: "accept-plan", Revision: 2}); err == nil {
		t.Fatal("accepted obsolete proposal")
	}
	f.must("dave", Command{Action: "accept-plan", Revision: 3})
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: a}); !errors.Is(err, ErrConflict) {
		t.Fatal("old plan resumed")
	}
	a = f.claim("alice", "item-1")
	result := f.result("alice", a, "done")
	f.review(result.SubmissionID, "accept")
	d.Items[0].AcceptanceCriteria = "silently reinterpret completed work"
	f.must("jerry", Command{Action: "propose", Definition: &d})
	if _, err := f.command("dave", Command{Action: "accept-plan", Revision: 4}); err == nil {
		t.Fatal("completed scope changed")
	}
	if f.view().Plan.AcceptedRevision != 3 {
		t.Fatal("failed acceptance mutated plan")
	}
}

func TestLeaseExpiryAndLateReview(t *testing.T) {
	f := newFixture(t)
	f.plan()
	a := f.claim("alice", "item-1")
	r := f.result("alice", a, "submitted before expiry")
	f.now = f.now.Add(AttemptTTL)
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: a}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired attempt revived")
	}
	f.claim("jerry", "item-1")
	if _, err := f.command("dave", Command{Action: "review", SubmissionID: r.SubmissionID, Decision: "accept", Reason: "review late", VerificationRefs: []string{"tests"}}); err == nil {
		t.Fatal("review trusted old receipt after takeover")
	}
	f.review(r.SubmissionID, "adopt")
}

func TestIdempotencyAndRedaction(t *testing.T) {
	f := newFixture(t)
	f.plan()
	a := f.claim("alice", "item-1")
	c := Command{SchemaVersion: 1, RequestID: "offline-upload", PlanID: "plan", Action: "submit", AttemptID: a, Kind: "checkpoint", Context: "password=hidden token='also-hidden' " + f.tokens["alice"], ArtifactRefs: []string{"https://user:secret@host/artifact"}}
	r, err := f.s.Execute(context.Background(), f.tokens["alice"], c)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.s.Execute(context.Background(), f.tokens["alice"], c)
	if err != nil || r != r2 {
		t.Fatalf("retry %+v %v", r2, err)
	}
	if len(f.view().Plan.Contributions) != 1 {
		t.Fatal("retry duplicated contribution")
	}
	c.Context = "different input"
	if _, err := f.s.Execute(context.Background(), f.tokens["alice"], c); err == nil {
		t.Fatal("idempotency key rebound")
	}
	var events string
	if err := f.s.db.QueryRow("SELECT command FROM events WHERE request_id='offline-upload'").Scan(&events); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(f.view())
	for _, secret := range []string{"hidden", "also-hidden", f.tokens["alice"], "user:secret@"} {
		if strings.Contains(events, secret) || strings.Contains(string(state), secret) {
			t.Fatalf("secret persisted: %q", secret)
		}
	}
	if !strings.Contains(events, "REDACTED") {
		t.Fatal("redaction marker missing")
	}
}

func TestInvalidDefinitionsAndDependencyReadiness(t *testing.T) {
	f := newFixture(t)
	f.plan()
	if _, err := f.command("alice", Command{Action: "claim", ItemID: "item-6", Machine: "pc", Agent: "agent"}); err == nil {
		t.Fatal("dependency ignored")
	}
	for _, items := range [][]Item{
		{{ID: "a", Title: "a", AcceptanceCriteria: "done", DependsOn: []string{"b"}}},
		{{ID: "a", Title: "a", AcceptanceCriteria: "done", DependsOn: []string{"a"}}},
		{{ID: "a", Title: "a", AcceptanceCriteria: "done"}, {ID: "a", Title: "a", AcceptanceCriteria: "done"}},
	} {
		if _, err := f.command("alice", Command{Action: "propose", Definition: &Definition{Title: "invalid", Items: items}}); err == nil {
			t.Fatal("invalid definition accepted")
		}
	}
}

func TestPrivateStorageAndNoAccidentalInitialization(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(dir, "team.db"), true); err == nil {
		t.Fatal("public storage accepted")
	}
	if _, err := Open(filepath.Join(dir, "missing.db"), false); err == nil {
		t.Fatal("missing DB accepted")
	}
	f := newFixture(t)
	if _, err := Open(f.path, true); err == nil {
		t.Fatal("existing DB overwritten")
	}
}

func TestForeignDatabaseIsNotMutated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foreign.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE other (value TEXT); INSERT INTO other VALUES ('preserve')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, false); err == nil {
		t.Fatal("foreign database accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("foreign database modified before validation")
	}
}

func TestAssignmentRoundTripNeverRevivesAttempt(t *testing.T) {
	f := newFixture(t)
	f.plan()
	f.must("dave", Command{Action: "assign", Engineer: "alice", Reason: "initial"})
	old := f.claim("alice", "item-1")
	f.must("dave", Command{Action: "assign", Engineer: "jerry", Reason: "handoff"})
	f.must("dave", Command{Action: "assign", Engineer: "alice", Reason: "return"})
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: old}); !errors.Is(err, ErrConflict) {
		t.Fatal("old epoch resurrected")
	}
	newAttempt := f.claim("alice", "item-1")
	if newAttempt == old {
		t.Fatal("attempt identity reused")
	}
	// Explicit scope and inherited scope must differ even with equal epoch values.
	f.must("dave", Command{Action: "assign", ItemID: "item-1", Engineer: "alice", Reason: "explicit child"})
	if _, err := f.command("alice", Command{Action: "resume", AttemptID: newAttempt}); !errors.Is(err, ErrConflict) {
		t.Fatal("scope change missed")
	}
}

func TestUsagePrecisionAndNonemptyEvidence(t *testing.T) {
	f := newFixture(t)
	f.plan()
	attempt := f.claim("alice", "item-1")
	const count int64 = 9007199254740993
	f.must("alice", Command{Action: "submit", AttemptID: attempt, Kind: "checkpoint", Context: "usage observation", Usage: &Usage{Provider: "provider", Model: "model", InputTokens: count, OutputTokens: 1, Confidence: "reported"}})
	if got := f.view().Plan.Contributions[0].Usage.InputTokens; got != count {
		t.Fatalf("integer usage rounded: %d", got)
	}
	if _, err := f.command("alice", Command{Action: "submit", AttemptID: attempt, Kind: "result", Context: "missing evidence", ArtifactRefs: []string{" "}, VerificationRefs: []string{""}}); err == nil {
		t.Fatal("blank evidence accepted")
	}
}
