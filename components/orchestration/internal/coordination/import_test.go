package coordination

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const sourceFixture = `{"schema_version":1,"project_id":"source-project","id":"accepted-plan","revision":7,"status":"active","title":"Existing work","summary":"Project context password=private","items":[{"id":"done","title":"Done","acceptance_criteria":"Tests pass","detail":"Finished work","status":"completed","evidence_refs":["commit:abc"]},{"id":"next","title":"Next","acceptance_criteria":"Integrates with done","status":"ready","depends_on":["done"]},{"id":"blocked","title":"Blocked","acceptance_criteria":"External dependency available","status":"blocked","progress_note":"Waiting for upstream"}]}`

func TestPrepareContinuityPreservesBaselineAndBlockers(t *testing.T) {
	f := newFixture(t)
	c, err := PrepareContinuity([]byte(sourceFixture), "plan", "import-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(c)
	if strings.Contains(string(body), "password=private") {
		t.Fatal("prepared source contains secret")
	}
	if _, err = f.s.Execute(context.Background(), f.tokens["dave"], c); err != nil {
		t.Fatal(err)
	}
	v := f.view()
	if v.Plan.AcceptedRevision != 0 || len(v.Ready) != 0 {
		t.Fatal("import released work without review")
	}
	f.must("dave", Command{Action: "accept-plan", Revision: 1})
	v = f.view()
	if v.Plan.Work["done"].CompletedBy != "source:done" || len(v.Ready) != 1 || v.Ready[0] != "next" {
		t.Fatalf("baseline lost: %+v", v)
	}
	if _, err := f.command("alice", Command{Action: "claim", ItemID: "blocked", Machine: "pc", Agent: "agent"}); err == nil {
		t.Fatal("source blocker ignored")
	}
	f.must("dave", Command{Action: "assign", Engineer: "alice", Reason: "first assignment"})
	f.must("dave", Command{Action: "assign", Engineer: "jerry", Reason: "handoff"})
	if f.view().Plan.Work["done"].CompletedBy != "source:done" {
		t.Fatal("imported completion changed on transfer")
	}
	history, err := f.s.History(context.Background(), f.tokens["jerry"], "plan", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 4 || history.Events[3].Actor != "dave" || history.Events[3].Command.Engineer != "jerry" {
		t.Fatal("assignment audit missing")
	}
	next, err := f.s.History(context.Background(), f.tokens["jerry"], "plan", history.NextAfter)
	if err != nil || len(next.Events) != 0 {
		t.Fatal("history cursor repeated events")
	}
	d := f.view().Plan.Revisions[0].Definition
	d.Items[1].PriorCompletionRefs = []string{"claim:done"}
	f.must("alice", Command{Action: "propose", Definition: &d})
	if _, err := f.command("dave", Command{Action: "accept-plan", Revision: 2}); err == nil {
		t.Fatal("later plan revision bypassed submission review")
	}
}

func TestPrepareRejectsLossyOrUnreviewedSources(t *testing.T) {
	for _, body := range []string{
		strings.Replace(sourceFixture, `"status":"active"`, `"status":"proposed"`, 1),
		strings.Replace(sourceFixture, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(sourceFixture, `"id":"next"`, `"id":"next","parent_id":"done"`, 1),
		strings.Replace(sourceFixture, `"evidence_refs":["commit:abc"]`, `"evidence_refs":[]`, 1),
		strings.Replace(sourceFixture, `"status":"ready"`, `"status":"superseded"`, 1),
		`{"id":"outline","revision":1,"status":"accepted"}`,
	} {
		if _, err := PrepareContinuity([]byte(body), "plan", "request"); err == nil {
			t.Fatal("lossy/unreviewed source accepted")
		}
	}
}

func TestRoutingPinsExactSourceWithoutRewritingBaseline(t *testing.T) {
	c, err := PrepareContinuity([]byte(sourceFixture), "plan", "routing")
	if err != nil {
		t.Fatal(err)
	}
	job := JobSpec{OS: "windows", Arch: "amd64", Priority: 10, Capabilities: []string{"native-tests"}, Repository: "repo", BaseRevision: "commit:abc", AllowedChanges: []string{"test fixtures only"}, ApprovalRequired: []string{"source publication"}}
	routing := Routing{SchemaVersion: 1, SourceRef: c.Definition.SourceRef, Jobs: map[string]JobSpec{"next": job}}
	body, _ := json.Marshal(routing)
	routed, err := ApplyRouting(c, body)
	if err != nil {
		t.Fatal(err)
	}
	if c.Definition.Items[1].Job != nil || routed.Definition.Items[1].Job == nil || routed.Definition.Items[1].ContextRefs[0] != c.Definition.Items[1].ContextRefs[0] {
		t.Fatal("routing mutated baseline or lost source identity")
	}
	f := newFixture(t)
	if _, err := f.s.Execute(context.Background(), f.tokens["dave"], routed); err != nil {
		t.Fatal(err)
	}
	q, err := f.s.Queue(context.Background(), f.tokens["alice"], QueueRequest{Worker: *windowsProfile(), PlanIDs: []string{"plan"}, Limit: 10})
	if err != nil || len(q.Jobs) != 0 {
		t.Fatal("proposal released routed work")
	}
	f.must("dave", Command{Action: "accept-plan", Revision: 1})
	q = queryQueue(t, f, "alice")
	if len(q.Jobs) != 1 || q.Jobs[0].Item.ID != "next" || q.Jobs[0].Item.Job.BaseRevision != "commit:abc" {
		t.Fatalf("routing lost on import: %+v", q)
	}
	for _, bad := range []Routing{{SchemaVersion: 1, SourceRef: "another-revision", Jobs: routing.Jobs}, {SchemaVersion: 1, SourceRef: routing.SourceRef, Jobs: map[string]JobSpec{"unknown": job}}, {SchemaVersion: 1, SourceRef: routing.SourceRef, Jobs: map[string]JobSpec{"done": job}}} {
		body, _ := json.Marshal(bad)
		if _, err := ApplyRouting(c, body); err == nil {
			t.Fatal("invalid routing accepted")
		}
	}
	if _, err := ApplyRouting(c, append(body, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing routing accepted")
	}
}
