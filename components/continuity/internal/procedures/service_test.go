package procedures

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func procedureFixture(projectID string) Procedure {
	return Procedure{
		ProjectID: projectID, ID: "go-checks", Title: "Go checks", Purpose: "Verify the component", Status: "accepted",
		Platforms: []string{"linux"}, WorkingDirectory: "components/continuity",
		Parameters:      []Parameter{{Name: "package", Description: "Package pattern", Required: true}},
		DeclaredEffects: []string{"build cache may change"}, Validations: []string{"commands exit zero"}, PostRunChecks: []string{"working tree remains attributable"},
		Steps:  []Step{{ID: "test", Description: "Run tests", StageKind: "source_check", StageID: "go-test", ArgvTemplate: []string{"go", "test", "{{package}}"}}},
		Source: "docs/procedure-monitoring.md", SourceRef: "HEAD",
	}
}

func TestAppendGetValidationAndIsolation(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := activity.Store{DB: db}
	projectID, err := store.EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Now: func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }}
	value := procedureFixture(projectID)
	value.Purpose = "Verify token=secret-value"
	recorded, err := service.Append(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Revision != 1 || recorded.Kind != "check" || recorded.Authority != "caller_asserted" || strings.Contains(recorded.Purpose, "secret-value") {
		t.Fatalf("unexpected procedure: %#v", recorded)
	}
	shown, err := service.Get(ctx, projectID, "go-checks", 0)
	if err != nil || shown == nil || shown.Revision != 1 || shown.Steps[0].StageID != "go-test" {
		t.Fatalf("unexpected latest accepted procedure: %#v %v", shown, err)
	}
	proposed := procedureFixture(projectID)
	proposed.Status, proposed.Title = "proposed", "Proposed revision"
	if _, err := service.Append(ctx, proposed); err != nil {
		t.Fatal(err)
	}
	shown, err = service.Get(ctx, projectID, "go-checks", 0)
	if err != nil || shown.Revision != 1 {
		t.Fatalf("proposed revision replaced latest accepted: %#v %v", shown, err)
	}
	otherProject, err := store.EnsureProject(ctx, filepath.Join(t.TempDir(), "other"), "other")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Get(ctx, otherProject, "go-checks", 1)
	if err != nil || other != nil {
		t.Fatalf("procedure leaked across projects: %#v %v", other, err)
	}
	invalid := procedureFixture(projectID)
	invalid.ID, invalid.WorkingDirectory = "invalid", "../outside"
	if _, err := service.Append(ctx, invalid); err == nil {
		t.Fatal("expected escaping working directory to fail")
	}
	invalid = procedureFixture(projectID)
	invalid.ID, invalid.Steps[0].StageKind = "wrong-kind", "commit_disposition"
	if _, err := service.Append(ctx, invalid); err == nil {
		t.Fatal("expected non-source-check selector to fail")
	}
}

func runbookFixture(projectID string) Procedure {
	return Procedure{
		ProjectID: projectID, ID: "test-box-install", Kind: "runbook", Title: "Install a build on the test box",
		Purpose: "Put the current commit's binary on the test box", Status: "proposed",
		Platforms: []string{"linux/amd64"}, WorkingDirectory: "components/continuity",
		Parameters:      []Parameter{{Name: "host", Description: "SSH destination", Required: true, ResourceKind: "host", Default: "resource:test-box"}},
		DeclaredEffects: []string{"replaces ~/.local/bin/continuity on the host"},
		Validations:     []string{"remote llewm version reports the local HEAD commit"},
		Steps: []Step{
			{ID: "build", Description: "Build a static binary", ArgvTemplate: []string{"go", "build", "-o", "{{out}}", "./cmd/llewm"}},
			{ID: "copy", Description: "Copy it to the host", ArgvTemplate: []string{"scp", "{{out}}", "{{host}}:.local/bin/continuity.new"}},
		},
		Source: "docs/resources-and-runbooks.md",
	}
}

func TestRunbookKindAndList(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(t.TempDir(), "repo"), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := Service{DB: db, Now: func() time.Time { return time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC) }}

	runbook, err := service.Append(ctx, runbookFixture(projectID))
	if err != nil || runbook.Kind != KindRunbook || runbook.Parameters[0].Default != "test-box" {
		t.Fatalf("runbook: %#v %v", runbook, err)
	}
	legacy := procedureFixture(projectID)
	legacy.Kind = "completion_check"
	if check, err := service.Append(ctx, legacy); err != nil || check.Kind != KindCheck {
		t.Fatalf("completion_check alias: %#v %v", check, err)
	}

	for name, mutate := range map[string]func(*Procedure){
		"runbook step with a stage":  func(p *Procedure) { p.Steps[0].StageKind, p.Steps[0].StageID = "source_check", "go-test" },
		"runbook without effects":    func(p *Procedure) { p.DeclaredEffects = nil },
		"check step without a stage": func(p *Procedure) { p.Kind, p.ID = "check", "bad-check" },
		"unknown kind":               func(p *Procedure) { p.Kind = "script" },
		"bad resource kind":          func(p *Procedure) { p.Parameters[0].ResourceKind = "database" },
	} {
		value := runbookFixture(projectID)
		value.ID = "invalid"
		mutate(&value)
		if _, err := service.Append(ctx, value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	if accepted, err := service.Get(ctx, projectID, "test-box-install", 0); err != nil || accepted != nil {
		t.Fatalf("latest accepted of a proposed-only runbook: %#v %v", accepted, err)
	}
	if latest, err := service.Latest(ctx, projectID, "test-box-install"); err != nil || latest == nil || latest.Status != "proposed" || latest.Revision != 1 {
		t.Fatalf("latest of any status: %#v %v", latest, err)
	}
	all, err := service.List(ctx, projectID, ListOptions{})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: %d %v", len(all), err)
	}
	runbooks, err := service.List(ctx, projectID, ListOptions{Kind: "runbook"})
	if err != nil || len(runbooks) != 1 || runbooks[0].ID != "test-box-install" {
		t.Fatalf("list runbooks: %#v %v", runbooks, err)
	}
	checks, err := service.List(ctx, projectID, ListOptions{Kind: "completion_check", Status: "accepted"})
	if err != nil || len(checks) != 1 || checks[0].ID != "go-checks" {
		t.Fatalf("list checks by old name: %#v %v", checks, err)
	}
	var hits int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE history_chunks MATCH 'scp' AND family='procedure' AND kind='runbook'`).Scan(&hits); err != nil || hits != 1 {
		t.Fatalf("runbook not searchable: %d %v", hits, err)
	}
}
