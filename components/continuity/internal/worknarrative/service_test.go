package worknarrative

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func testService(t *testing.T) (context.Context, Service, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, Service{DB: db, Root: root, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}, projectID, root
}

func sourceHash() string {
	sum := sha256.Sum256([]byte("first\n"))
	return hex.EncodeToString(sum[:])
}

func TestAppendListShowDriftAndSupersession(t *testing.T) {
	ctx, service, projectID, root := testService(t)
	from := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	first, err := service.Append(ctx, Record{ID: "episode", ProjectID: projectID, Title: "Decision token=secret-value", Trigger: "accepted_decision", OccurredFrom: &from, OccurredTo: &to, Creator: "codex", Claims: []Claim{{Kind: "intent", Text: "Choose safely token=claim-secret", Authority: "caller_asserted"}, {Kind: "verification", Text: "Observed exact bytes", Authority: "machine_observed"}}, Anchors: []Anchor{{Kind: "checkpoint", Target: "checkpoint-a", Authority: "caller_asserted"}, {Kind: "checkpoint", Target: "checkpoint-b", Authority: "caller_asserted"}, {Kind: "file", Target: "source.md", ContentSHA256: sourceHash(), Authority: "machine_observed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Title, "secret-value") || strings.Contains(first.Claims[0].Text, "claim-secret") {
		t.Fatalf("record was not redacted: %#v", first)
	}
	listed, err := service.List(ctx, projectID, ListOptions{RecordType: RecordType, AnchorKind: "checkpoint", Anchor: "checkpoint-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Records) != 1 || listed.Measurement.RecordsLoaded != 0 || len(listed.Records[0].Anchors) != 3 {
		t.Fatalf("unexpected list: %#v", listed)
	}
	shown, err := service.Show(ctx, projectID, "narrative:episode", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if shown == nil || shown.Record.Anchors[2].Drift != "current" || shown.Measurement.RecordsLoaded != 1 {
		t.Fatalf("unexpected show: %#v", shown)
	}
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err = service.Show(ctx, projectID, "episode", 4096)
	if err != nil || shown.Record.Anchors[2].Drift != "drifted" {
		t.Fatalf("expected drift: %#v %v", shown, err)
	}
	second, err := service.Append(ctx, Record{ID: "episode-2", ProjectID: projectID, Title: "Corrected", Trigger: "manual", Creator: "human", SupersedesID: first.ID, Claims: []Claim{{Kind: "decision", Text: "Use corrected account", Authority: "user_reported"}}})
	if err != nil || second.State != "current" {
		t.Fatalf("supersession failed: %#v %v", second, err)
	}
	shown, err = service.Show(ctx, projectID, first.ID, 4096)
	if err != nil || shown.Record.State != "superseded" {
		t.Fatalf("old state: %#v %v", shown, err)
	}
	if _, err = service.Append(ctx, Record{ID: "episode-3", ProjectID: projectID, Title: "Second correction", Trigger: "manual", Creator: "human", SupersedesID: first.ID, Claims: []Claim{{Kind: "decision", Text: "again", Authority: "caller_asserted"}}}); err == nil {
		t.Fatal("expected one-successor refusal")
	}
}

func TestProjectIsolationAndValidation(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	otherRoot := t.TempDir()
	otherID, err := (activity.Store{DB: service.DB}).EnsureProject(ctx, otherRoot, "other")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Append(ctx, Record{ID: "one", ProjectID: projectID, Title: "One", Trigger: "manual", Creator: "human", Claims: []Claim{{Kind: "outcome", Text: "done", Authority: "caller_asserted"}}})
	if err != nil {
		t.Fatal(err)
	}
	if shown, err := service.Show(ctx, otherID, "one", 1024); err != nil || shown != nil {
		t.Fatalf("cross-project show: %#v %v", shown, err)
	}
	if _, err = service.Append(ctx, Record{ProjectID: projectID, Title: "Bad", Trigger: "manual", Creator: "human", Claims: []Claim{{Kind: "outcome", Text: "bad", Authority: "unknown"}}}); err == nil {
		t.Fatal("expected authority validation")
	}
	if _, err = service.Append(ctx, Record{ProjectID: projectID, Title: "Bad path", Trigger: "manual", Creator: "human", Claims: []Claim{{Kind: "outcome", Text: "bad", Authority: "caller_asserted"}}, Anchors: []Anchor{{Kind: "file", Target: "../escape", ContentSHA256: sourceHash(), Authority: "machine_observed"}}}); err == nil {
		t.Fatal("expected path validation")
	}
}

func TestListAnchorFiltersWithoutKindOrTarget(t *testing.T) {
	ctx, service, projectID, _ := testService(t)
	commit := strings.Repeat("a", 40)
	if _, err := service.Append(ctx, Record{ID: "work", ProjectID: projectID, Title: "Work", Trigger: "manual", Creator: "human", Claims: []Claim{{Kind: "outcome", Text: "done", Authority: "caller_asserted"}}, Anchors: []Anchor{{Kind: "checkpoint", Target: "checkpoint-a", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Append(ctx, Record{ID: "evidence", ProjectID: projectID, Title: "Evidence", Trigger: "manual", Creator: "human", Claims: []Claim{{Kind: "outcome", Text: "committed", Authority: "caller_asserted"}}, Anchors: []Anchor{{Kind: "commit", Target: commit, Authority: "caller_asserted"}, {Kind: "task", Target: "task-a", Authority: "caller_asserted"}, {Kind: "file", Target: "source.md", ContentSHA256: strings.Repeat("b", 64), Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		options ListOptions
		want    string
	}{
		{ListOptions{Anchor: "checkpoint-a"}, "work"},
		{ListOptions{Anchor: commit}, "evidence"},
		{ListOptions{Anchor: "task-a"}, "evidence"},
		{ListOptions{Anchor: "source.md"}, "evidence"},
		{ListOptions{Anchor: "missing"}, ""},
		{ListOptions{AnchorKind: "commit"}, "evidence"},
		{ListOptions{AnchorKind: "task"}, "evidence"},
		{ListOptions{AnchorKind: "file"}, "evidence"},
		{ListOptions{AnchorKind: "checkpoint"}, "work"},
		{ListOptions{AnchorKind: "task", Anchor: commit}, ""},
		{ListOptions{}, "evidence,work"},
	} {
		listed, err := service.List(ctx, projectID, test.options)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, record := range listed.Records {
			ids = append(ids, record.ID)
		}
		sort.Strings(ids)
		if got := strings.Join(ids, ","); got != test.want {
			t.Errorf("%+v listed %q, want %q", test.options, got, test.want)
		}
	}
}
