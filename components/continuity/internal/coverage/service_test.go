package coverage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

const requirementsText = `# Requirements

Intro text before the first marker is ignored.

<!-- continuity:requirement session-start -->
## Session start
scope: claude-code, codex
A fresh session receives the bootstrap.

<!-- continuity:requirement checkpoint-hook -->
## Checkpoint hook
Hooks record events.

<!-- continuity:requirement no-title -->
Body without a heading.

<!-- continuity:requirement session-start -->
## Duplicate
`

func testService(t *testing.T) (context.Context, Service, string, *time.Time) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := activity.Store{DB: db}.EnsureProject(ctx, "/first", "first")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, DefaultRequirementsPath), []byte(requirementsText), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Root: root, Now: func() time.Time { return now }}, projectID, &now
}

func cell(projectID, requirement, client, status, verification string) Cell {
	return Cell{ProjectID: projectID, RequirementID: requirement, Client: client, Platform: "linux", ClientVersion: "1.0", Status: status, Verification: verification, Creator: "tester"}
}

func TestParseRequirementsReportsProblemsAndKeepsValidSections(t *testing.T) {
	reqs, problems := ParseRequirements(requirementsText)
	if len(reqs) != 2 || reqs[0].ID != "session-start" || reqs[0].Title != "Session start" || strings.Join(reqs[0].Scope, ",") != "claude-code,codex" || reqs[0].Body != "A fresh session receives the bootstrap." {
		t.Fatalf("unexpected requirements: %#v", reqs)
	}
	if reqs[1].ID != "checkpoint-hook" || len(reqs[1].Scope) != 0 {
		t.Fatalf("unexpected second requirement: %#v", reqs[1])
	}
	if len(problems) != 2 || !strings.Contains(problems[0], `"no-title" has no heading`) || !strings.Contains(problems[1], `duplicate requirement "session-start"`) {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestRecordRequiresKnownRequirementAndRedacts(t *testing.T) {
	ctx, s, projectID, _ := testService(t)
	if _, err := s.Record(ctx, "", cell(projectID, "missing", "codex", "supported", "observed")); err == nil || !strings.Contains(err.Error(), "not in the requirements file") {
		t.Fatalf("expected unknown requirement refusal, got %v", err)
	}
	for _, bad := range []Cell{cell(projectID, "session-start", "codex", "documented", "observed"), cell(projectID, "session-start", "codex", "supported", "claimed"), cell(projectID, "session-start", "", "supported", "observed")} {
		if _, err := s.Record(ctx, "", bad); err == nil {
			t.Fatalf("accepted invalid cell %#v", bad)
		}
	}
	c := cell(projectID, "session-start", "codex", "supported", "observed")
	c.Implementation = "hook token=coverage-secret"
	c.Refs = []string{"commit:b", "commit:a", "commit:a"}
	created, err := s.Record(ctx, "", c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created.Implementation, "coverage-secret") || strings.Join(created.Refs, ",") != "commit:a,commit:b" {
		t.Fatalf("redaction or ref normalization failed: %#v", created)
	}
}

func TestReportShowsUnknownLatestCellOrphansAndHistory(t *testing.T) {
	ctx, s, projectID, now := testService(t)
	record := func(c Cell) Cell {
		*now = now.Add(time.Minute)
		created, err := s.Record(ctx, "", c)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	record(cell(projectID, "session-start", "codex", "failing", "observed"))
	latest := record(cell(projectID, "session-start", "codex", "supported", "tested"))
	record(cell(projectID, "session-start", "claude-code", "partial", "inferred"))
	// A requirement removed from the file leaves an orphaned cell.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO coverage_cells(id,project_id,requirement_id,client,platform,status,verification,refs_json,creator,authority,recorded_at,schema_version) VALUES('orphan',?,'removed','codex','linux','supported','observed','[]','tester','caller_asserted',?,1)`, projectID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(ctx, projectID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != 2 || report.Rows[0].ID != "session-start" || report.Rows[0].Status != "partial" || len(report.Rows[0].Cells) != 2 {
		t.Fatalf("unexpected rows: %#v", report.Rows)
	}
	for _, c := range report.Rows[0].Cells {
		if c.Client == "codex" && c.ID != latest.ID {
			t.Fatalf("current codex cell is %s, want latest %s", c.ID, latest.ID)
		}
	}
	if report.Rows[1].ID != "checkpoint-hook" || report.Rows[1].Status != "unknown" || len(report.Rows[1].Cells) != 0 {
		t.Fatalf("unrecorded requirement should be unknown: %#v", report.Rows[1])
	}
	if len(report.Orphaned) != 1 || report.Orphaned[0].RequirementID != "removed" || len(report.Problems) != 2 {
		t.Fatalf("orphans or problems wrong: %#v %v", report.Orphaned, report.Problems)
	}
	filtered, err := s.Report(ctx, projectID, Options{Client: "codex", Status: "supported", Limit: 1})
	if err != nil || filtered.Total != 1 || filtered.Rows[0].Status != "supported" {
		t.Fatalf("filter failed: %#v %v", filtered, err)
	}
	history, err := s.History(ctx, projectID, "session-start")
	if err != nil || len(history) != 3 || history[0].ID == "" {
		t.Fatalf("history: %d %v", len(history), err)
	}
	if _, err = s.DB.ExecContext(ctx, `UPDATE coverage_cells SET status='failing'`); err == nil {
		t.Fatal("cells must be append-only")
	}
}

func TestStaleCellsCompareToLastObservedClientVersion(t *testing.T) {
	ctx, s, projectID, now := testService(t)
	if _, err := s.Record(ctx, "", cell(projectID, "session-start", "codex", "supported", "observed")); err != nil {
		t.Fatal(err)
	}
	for i, version := range []string{"1.0", "1.1"} {
		at := now.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO actor_invocations(id,project_id,actor_type,client,client_version,identity_source,occurred_at,received_at,schema_version) VALUES(?,?,'agent','codex',?,'client_hook_claim',?,?,1)`, "inv-"+version, projectID, version, at, at); err != nil {
			t.Fatal(err)
		}
	}
	stale, err := s.StaleCells(ctx, projectID)
	if err != nil || len(stale) != 1 || stale[0].Installed != "1.1" || stale[0].Cell.ClientVersion != "1.0" {
		t.Fatalf("stale: %#v %v", stale, err)
	}
}
