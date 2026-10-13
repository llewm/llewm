package brief

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/decision"
)

func TestTaskBriefUsesExactTipAndCurrentDirectDecisions(t *testing.T) {
	ctx, db, projectID := setup(t)
	checkpoints := checkpoint.Service{DB: db}
	cp, err := checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "sync", Task: "Sync", Summary: "Known state", NextAction: "Continue sync-plan/squash", Verified: []string{"Go tests passed at commit abc"}, Attempts: []checkpoint.Attempt{{Description: "Try old route", Outcome: "failed", Reason: "bad source"}}, EvidenceRefs: []string{"commit:abc", "commit:abc"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = checkpoints.Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "unrelated", Task: "Other", Summary: "Should not leak", NextAction: "Other action"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "current"} {
		r := decision.Record{ID: id, ProjectID: projectID, Title: id, Rationale: "Fixture decision", Disposition: "accepted", Actor: "qj", ActorType: "human", Authority: "user_reported", ReviewSource: "fixture", Links: []decision.Link{{Kind: "subject", TargetKind: "task", TargetID: "sync"}}}
		if id == "current" {
			r.SupersedesID = "old"
		}
		if _, err := (decision.Service{DB: db}).Append(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	b, err := BuildTask(ctx, db, projectID, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if b.CheckpointRef != "checkpoint:"+cp.ID || b.Summary != "Known state" || len(b.Verified) != 1 || len(b.Attempts) != 1 || len(b.Items) != 1 || len(b.Decisions) != 1 || b.Decisions[0].ID != "current" || len(b.EvidenceRefs) != 1 {
		t.Fatalf("brief: %+v", b)
	}
	if strings.Contains(TaskMarkdown(b), "Should not leak") || !strings.Contains(TaskMarkdown(b), "not live verification") {
		t.Fatal(TaskMarkdown(b))
	}
	if _, err := BuildTask(ctx, db, projectID, "missing"); err == nil {
		t.Fatal("missing task silently substituted")
	}
}

func TestTaskBriefBoundsBothFormatsAndPreservesExactReference(t *testing.T) {
	b := TaskBrief{SchemaVersion: 1, TaskID: "large", CheckpointRef: "checkpoint:exact", Task: "Task", Status: "in_progress", Summary: strings.Repeat("é", 6000), NextAction: strings.Repeat("\u0001", 2000), Verified: []string{strings.Repeat("large", 2000)}, Omitted: map[string]int{}}
	got, err := boundTask(b)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.MarshalIndent(got, "", "  ")
	text := TaskMarkdown(got)
	if len(body) > MaxBytes || len(text) > MaxBytes || !utf8.ValidString(text) || got.CheckpointRef != "checkpoint:exact" || len(got.Omitted) == 0 {
		t.Fatalf("budget: JSON %d text %d %+v", len(body), len(text), got.Omitted)
	}
}
