package usage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestCompareKeepsCachesAndReasoningSeparateAndUsesExactSessionScope(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := (activity.Store{DB: db}).EnsureProject(ctx, "/fixture", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		session                         string
		input, cache, output, sidechain int
	}{{"baseline", 200, 800, 50, 0}, {"candidate", 100, 400, 25, 0}, {"candidate", 20, 80, 5, 1}} {
		_, err = db.Exec(`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'codex',?,?,'fixture-model',?, ?,0,?,?,5,?)`, id, s.session, s.session+string(rune('0'+s.sidechain)), "2026-10-12T10:00:0"+string(rune('0'+s.sidechain))+"Z", s.input, s.cache, s.output, s.sidechain)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Identical ID under another client must not bleed into the chosen run.
	_, err = db.Exec(`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES(?,'claude-code','baseline','other','other-model','2026-10-12T10:00:00Z',9999,0,0,0,0,0)`, id)
	if err != nil {
		t.Fatal(err)
	}
	r := Reporter{DB: db, ProjectID: id}
	c, err := r.Compare(ctx, RunRef{Codex, "baseline"}, RunRef{Codex, "candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Baseline.Tokens.Input != 200 || c.Candidate.Tokens.Input != 120 || c.Candidate.Tokens.Requests != 2 || c.Candidate.StartupPrompt == nil || *c.Candidate.StartupPrompt != 500 {
		t.Fatalf("scope: %+v", c)
	}
	metrics := map[string]Metric{}
	for _, m := range c.Metrics {
		metrics[m.Name] = m
	}
	if metrics["total_tokens"].Baseline != 1050 || metrics["total_tokens"].Candidate != 630 || metrics["total_tokens"].Delta != -420 || metrics["cache_read_tokens"].Candidate != 480 {
		t.Fatalf("totals: %+v", metrics)
	}
	if metrics["cache_write_tokens"].ChangePercent != nil {
		t.Fatal("zero baseline percentage should be null")
	}
	if _, ok := metrics["estimated_cost_usd"]; ok {
		t.Fatal("unpriced cost comparison invented")
	}
	if _, err := r.Compare(ctx, RunRef{Codex, "missing"}, RunRef{Codex, "candidate"}); err == nil {
		t.Fatal("missing baseline accepted")
	}
	if _, err := r.Compare(ctx, RunRef{Codex, "baseline"}, RunRef{Codex, "baseline"}); err == nil {
		t.Fatal("self-comparison accepted")
	}
	if _, err := r.Report(ctx, Options{Session: "baseline"}); err == nil {
		t.Fatal("unqualified session accepted")
	}
	report, err := r.Report(ctx, Options{Client: Codex, Session: "baseline"})
	if err != nil || report.Total.Input != 200 {
		t.Fatalf("report filter: %+v %v", report, err)
	}
	if !strings.Contains(c.Disclaimer, "not causal savings") {
		t.Fatal("missing comparison limits")
	}
}

func TestRunRefValidation(t *testing.T) {
	for _, s := range []string{"", "codex:", "other:s", "codex:a/b", "codex:a:b"} {
		if _, err := ParseRunRef(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	if ref, err := ParseRunRef("codex:session-1"); err != nil || ref.Session != "session-1" {
		t.Fatalf("%+v %v", ref, err)
	}
}
