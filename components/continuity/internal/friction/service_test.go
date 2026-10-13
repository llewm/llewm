package friction

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
)

func frictionTestService(t *testing.T) (context.Context, Service, activity.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := activity.Store{DB: db}
	first, err := store.EnsureProject(ctx, "/first", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureProject(ctx, "/second", "second")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	return ctx, Service{DB: db, Now: func() time.Time { return now }}, store, first, second
}

func appendTool(t *testing.T, ctx context.Context, store activity.Store, projectID, id, sessionID, client, command, outcome, signature string, at time.Time) {
	t.Helper()
	payload := map[string]any{"tool": "Bash", "command": command}
	if signature != "" {
		payload["error_signature"] = signature
	}
	event := activity.Event{ID: id, ProjectID: projectID, SessionID: sessionID, TaskID: "task-cache", Client: client, EventType: "posttooluse", Category: "tool", ActorType: "cloud_model", Outcome: outcome, Payload: payload, OccurredAt: at, ReceivedAt: at, SchemaVersion: 1}
	if outcome == "failure" {
		event.EventType = "posttoolusefailure"
	}
	if err := store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
}

func TestReportGroupsRepeatedGoCacheRecoveryAndTracking(t *testing.T) {
	ctx, service, store, projectID, otherProject := frictionTestService(t)
	base := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	for i, client := range []string{"codex", "claude-code"} {
		session := fmt.Sprintf("session-%d", i)
		at := base.Add(time.Duration(i) * time.Hour)
		appendTool(t, ctx, store, projectID, fmt.Sprintf("failure-%d", i), session, client, "go test ./...", "failure", "go: failed to initialize build cache: permission denied token=fixture-friction-secret", at)
		appendTool(t, ctx, store, projectID, fmt.Sprintf("recovery-%d", i), session, client, "GOCACHE=/tmp/llewm-go-cache go test ./...", "", "", at.Add(time.Second))
	}
	appendTool(t, ctx, store, otherProject, "other-failure", "other-session", "codex", "go test ./...", "failure", "go: failed to initialize build cache: permission denied", base)
	appendTool(t, ctx, store, otherProject, "other-recovery", "other-session", "codex", "GOCACHE=/tmp/other go test ./...", "", "", base.Add(time.Second))

	report, err := service.Report(ctx, projectID, Options{MinCount: 2, MaxBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if report.CandidateCount != 1 || report.IncludedCount != 1 || report.Truncated || report.EventsScanned != 4 || len(report.Candidates) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
	candidate := report.Candidates[0]
	if candidate.CommandFamily != "go test" || candidate.RecoveryShape != "GOCACHE=* go test" || candidate.OccurrenceCount != 2 || candidate.Cause != "unknown" {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
	if strings.Contains(candidate.FailureSignature, "fixture-friction-secret") || !strings.Contains(candidate.FailureSignature, "[redacted]") {
		t.Fatalf("failure signature was not redacted: %q", candidate.FailureSignature)
	}
	if len(candidate.Clients) != 2 || len(candidate.TaskIDs) != 1 || len(candidate.ObservationRefs) != 4 {
		t.Fatalf("candidate dimensions missing: %#v", candidate)
	}
	if strings.Contains(report.Markdown, "other-failure") || !strings.Contains(report.Markdown, "friction:"+candidate.ID) {
		t.Fatalf("project isolation/reference failure: %s", report.Markdown)
	}

	if !strings.Contains(report.Markdown, "Tracked by: none") {
		t.Fatalf("untracked candidate does not say so: %s", report.Markdown)
	}
	items := workitem.Service{DB: service.DB, Now: service.Now}
	bug, err := items.Capture(ctx, workitem.Capture{Item: workitem.Item{ProjectID: projectID, Kind: "bug", Title: "Go cache permission failures", Body: "Repeated cache failures", Creator: "human"}, From: "friction:" + candidate.ID})
	if err != nil {
		t.Fatal(err)
	}
	report, err = service.Report(ctx, projectID, Options{MinCount: 2, MaxBytes: 2048})
	if err != nil || len(report.Candidates[0].TrackedBy) != 1 || report.Candidates[0].TrackedBy[0].Ref != workitem.Ref(bug.ID) || report.Candidates[0].TrackedBy[0].Status != "pending" || !strings.Contains(report.Markdown, workitem.Ref(bug.ID)) {
		t.Fatalf("tracking work item missing: %#v %v", report, err)
	}
}

func TestReportBounds(t *testing.T) {
	ctx, service, store, projectID, _ := frictionTestService(t)
	base := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		session := fmt.Sprintf("session-%d", i)
		at := base.Add(time.Duration(i) * time.Minute)
		appendTool(t, ctx, store, projectID, fmt.Sprintf("failure-%d", i), session, "codex", fmt.Sprintf("tool%d run", i), "failure", strings.Repeat("failure ", 100), at)
		appendTool(t, ctx, store, projectID, fmt.Sprintf("recovery-%d", i), session, "codex", fmt.Sprintf("CACHE_%d=/tmp/cache tool%d run", i, i), "", "", at.Add(time.Second))
	}
	report, err := service.Report(ctx, projectID, Options{MinCount: 1, Limit: 100, MaxBytes: 512})
	if err != nil || !report.Truncated || report.RenderedBytes > 512 || report.IncludedCount >= report.CandidateCount {
		t.Fatalf("report was not bounded: %#v %v", report, err)
	}
	if _, err = service.Report(ctx, projectID, Options{MinCount: 0, MaxBytes: 511}); err == nil {
		t.Fatal("invalid report budget succeeded")
	}
}

func TestEmptyReportUsesEmptyCandidateArray(t *testing.T) {
	ctx, service, _, projectID, _ := frictionTestService(t)
	report, err := service.Report(ctx, projectID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Candidates == nil || len(report.Candidates) != 0 || report.CandidateCount != 0 {
		t.Fatalf("empty report did not preserve array shape: %#v", report)
	}
}
