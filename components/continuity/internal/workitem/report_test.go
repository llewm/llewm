package workitem

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBugReportIsBoundedRedactedReadOnlyAndProjectScoped(t *testing.T) {
	ctx, s, first, second := testService(t)
	bug := capture(t, ctx, s, Item{ID: "bug", ProjectID: first, Kind: "bug", Title: "Hook failed", Body: "token=body-secret", Observed: "password=observed-secret", Reproduction: "Start a fresh session", Environment: "Linux; api_key=environment-secret", Severity: "high", Creator: "agent", Model: "model", Authority: "model_suggested", TaskID: "private-task", CheckpointID: "private-checkpoint"})
	capture(t, ctx, s, Item{ID: "unrelated", ProjectID: first, Kind: "idea", Title: "Unrelated", Body: "unrelated-private-body", Creator: "human"})
	options := ReportOptions{Expected: "Load bootstrap token=expected-secret", Hypothesis: "Maybe password=hypothesis-secret"}
	r, err := s.Report(ctx, first, Ref(bug.ID), options)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(r)
	for _, forbidden := range []string{"body-secret", "observed-secret", "environment-secret", "expected-secret", "hypothesis-secret", "private-task", "private-checkpoint", "unrelated-private-body", "executable", "dispositions", "links"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("report leaked %q", forbidden)
		}
	}
	if r.Source.Authority != "model_suggested" || len(r.Missing) != 0 || !strings.Contains(r.Markdown(), "Hypothesis (unverified)") {
		t.Fatalf("report lost provenance or context: %#v", r)
	}
	repeated, err := s.Report(ctx, first, bug.ID, options)
	if err != nil || repeated.ReportID != r.ReportID {
		t.Fatalf("identical report changed identity: %#v %v", repeated, err)
	}
	changed, err := s.Report(ctx, first, bug.ID, ReportOptions{Expected: "Different claim"})
	if err != nil || changed.ReportID == r.ReportID {
		t.Fatalf("changed report reused identity: %#v %v", changed, err)
	}
	stored, err := s.Get(ctx, first, bug.ID)
	if err != nil || stored.Body != bug.Body || len(stored.Dispositions) != 0 {
		t.Fatalf("report changed source: %#v %v", stored, err)
	}
	if _, err := s.Report(ctx, second, bug.ID, options); err == nil {
		t.Fatal("cross-project report succeeded")
	}
	if _, err := s.Report(ctx, first, "unrelated", options); err == nil {
		t.Fatal("non-bug report succeeded")
	}
}

func TestBugReportMissingFieldsAndOversize(t *testing.T) {
	ctx, s, project, _ := testService(t)
	capture(t, ctx, s, Item{ID: "incomplete", ProjectID: project, Kind: "bug", Title: "Failure", Body: "Unknown cause", Creator: "human"})
	r, err := s.Report(ctx, project, "incomplete", ReportOptions{})
	if err != nil || strings.Join(r.Missing, ",") != "observed,expected,reproduction,environment,severity" {
		t.Fatalf("missing evidence was invented: %#v %v", r, err)
	}
	capture(t, ctx, s, Item{ID: "large", ProjectID: project, Kind: "bug", Title: "Failure", Body: strings.Repeat("x", MaxBytes), Observed: strings.Repeat("x", MaxBytes), Creator: "human"})
	if _, err := s.Report(ctx, project, "large", ReportOptions{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize report not rejected intact: %v", err)
	}
	if _, err := s.Report(ctx, project, "incomplete", ReportOptions{Expected: strings.Repeat("x", MaxBytes+1)}); err == nil {
		t.Fatal("oversize supplement accepted")
	}
}
