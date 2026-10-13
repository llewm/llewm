package brief

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/standinggrant"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func setup(t *testing.T) (context.Context, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	plans := trackplan.Service{DB: db}
	for _, plan := range []trackplan.Plan{
		{ProjectID: projectID, ID: "sync-plan", RoadmapNodeID: "sync", TaskID: "sync", Title: "Sync", Summary: "Sync slices", Status: "active", Creator: "human-review", Items: []trackplan.Item{
			{ID: "code-behind", Position: 0, Title: "4a: Code-behind detection", Status: "completed"},
			{ID: "squash", Position: 1, Title: "4e: Squash-merge detection", Detail: "Report squash merges. Later follow-ups exist.", Status: "pending", DependsOn: []string{"code-behind"},
				EvidenceRefs: []string{"code:internal/app/code.go#codeBehind", "docs:docs/sync.md", "procedure:missing", "commit:abc"}},
			{ID: "fencing", Position: 2, Title: "5b: Lease fencing", Detail: "Record lease tokens.", Status: "pending"},
		}},
		{ProjectID: projectID, ID: "other-plan", RoadmapNodeID: "other", Title: "Other", Summary: "Other slices", Status: "active", Creator: "human-review", Items: []trackplan.Item{
			{ID: "squash", Position: 0, Title: "Squash elsewhere", Status: "completed"},
			{ID: "fencing-old", Position: 1, Title: "5b: Earlier fencing", Status: "pending"},
		}},
	} {
		if _, err := plans.AppendPlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, db, projectID
}

func TestLabel(t *testing.T) {
	for title, want := range map[string]string{"4e: Squash": "4e", "5a2: Branch": "5a2", "Squash": "", "4: No letter": "", "e4: Letter first": ""} {
		if got := Label(title); got != want {
			t.Errorf("Label(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	ctx, db, projectID := setup(t)
	for ref, want := range map[string]string{
		"4e":                     "sync-plan/squash",
		"4E":                     "sync-plan/squash",
		"sync-plan/squash":       "sync-plan/squash",
		"item:code-behind":       "sync-plan/code-behind",
		"other-plan/squash":      "other-plan/squash",
		"squash":                 "sync-plan/squash", // the other one is completed
		"sync-plan/5b":           "sync-plan/fencing",
		"other-plan/fencing-old": "other-plan/fencing-old",
	} {
		got, err := Resolve(ctx, db, projectID, ref)
		if err != nil || got.Ref() != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got.Ref(), err, want)
		}
	}
	var ambiguous *AmbiguousError
	if _, err := Resolve(ctx, db, projectID, "5b"); !errors.As(err, &ambiguous) || len(ambiguous.Matches) != 2 {
		t.Fatalf("5b should be ambiguous across plans: %v", err)
	}
	if _, err := Resolve(ctx, db, projectID, "9z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown label: %v", err)
	}
}

func TestBuildAndMarkdown(t *testing.T) {
	ctx, db, projectID := setup(t)
	value, err := Build(ctx, db, projectID, "4e")
	if err != nil {
		t.Fatal(err)
	}
	if value.Label != "4e" || value.PlanRevision != 1 || len(value.DependsOn) != 1 || value.DependsOn[0].Status != "completed" || value.DependsOn[0].Label != "4a" {
		t.Fatalf("brief: %+v", value)
	}
	if len(value.StartingPoints) != 1 || value.StartingPoints[0] != "internal/app/code.go#codeBehind" || len(value.Docs) != 1 || len(value.Procedures) != 1 || value.Procedures[0].Title != "" || len(value.OtherEvidence) != 1 {
		t.Fatalf("evidence split: %+v", value)
	}
	text := Markdown(value)
	for _, want := range []string{"# Brief: 4e squash [pending]", "## Goal and done-when", "- 4a code-behind: completed", "## Starting points", "- internal/app/code.go#codeBehind", "- procedure:missing", "Docs: docs/sync.md", "Evidence: commit:abc"} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown lacks %q:\n%s", want, text)
		}
	}
	value.Detail = strings.Repeat("x", MaxBytes)
	if got := Markdown(value); len(got) > MaxBytes || !strings.HasSuffix(got, "[truncated]\n") {
		t.Fatalf("markdown not bounded: %d bytes", len(got))
	}
}

func TestMentioned(t *testing.T) {
	ctx, db, projectID := setup(t)
	items, err := Mentioned(ctx, db, projectID, "Engineer picks 4e or item:fencing; 4a is done, and v4e or 4e-2 are not labels.")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, item := range items {
		refs = append(refs, item.Ref())
	}
	// 4a is completed; 5b in other-plan is not mentioned.
	if strings.Join(refs, ",") != "sync-plan/squash,sync-plan/fencing" {
		t.Fatalf("mentioned: %v", refs)
	}
	if line := Line(items[0]); line != "4e squash [pending]: Report squash merges. (llewm brief 4e)" {
		t.Fatalf("line: %q", line)
	}
	if items, err := Mentioned(ctx, db, projectID, ""); err != nil || items != nil {
		t.Fatalf("empty text: %v %v", items, err)
	}
}

func TestBuildListsStandingGrants(t *testing.T) {
	ctx, db, projectID := setup(t)
	if _, err := (procedures.Service{DB: db}).Append(ctx, procedures.Procedure{ProjectID: projectID, ID: "missing", Title: "Install", Purpose: "Install the build", Kind: procedures.KindRunbook, Status: "accepted",
		Platforms: []string{"linux/amd64"}, DeclaredEffects: []string{"replaces the binary"}, Steps: []procedures.Step{{ID: "run", Description: "Run it"}}, Source: "docs/installation.md"}); err != nil {
		t.Fatal(err)
	}
	grant, err := (standinggrant.Service{DB: db}).Create(ctx, standinggrant.Request{ProjectID: projectID, ProcedureID: "missing", Creator: "engineer"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := Build(ctx, db, projectID, "4e")
	if err != nil {
		t.Fatal(err)
	}
	want := "- procedure:missing: Install (standing grant " + grant.ID + ": procedure:missing@1 after slice commit)"
	if text := Markdown(value); !strings.Contains(text, want) {
		t.Fatalf("markdown lacks %q:\n%s", want, text)
	}
}

func TestMentionedOrPending(t *testing.T) {
	ctx, db, projectID := setup(t)
	refs := func(items []Candidate) string {
		var out []string
		for _, item := range items {
			out = append(out, item.Ref())
		}
		return strings.Join(out, ",")
	}
	pending := []string{"Ask the user to pick 4e or item:fencing."}
	items, err := MentionedOrPending(ctx, db, projectID, "Record more cells.", pending)
	if err != nil || refs(items) != "sync-plan/squash,sync-plan/fencing" {
		t.Fatalf("fallback: %v %v", refs(items), err)
	}
	items, err = MentionedOrPending(ctx, db, projectID, "Start item:fencing.", pending)
	if err != nil || refs(items) != "sync-plan/fencing" {
		t.Fatalf("next action wins: %v %v", refs(items), err)
	}
	if items, err = MentionedOrPending(ctx, db, projectID, "", nil); err != nil || items != nil {
		t.Fatalf("empty: %v %v", items, err)
	}
}
