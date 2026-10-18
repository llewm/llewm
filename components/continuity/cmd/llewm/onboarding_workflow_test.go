package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/proseimport"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

func TestDoctorStateIsReadOnlyWithoutStoreAndAfterExit(t *testing.T) {
	root := testProject(t)
	cmd := newRootCommand("llewm")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"doctor", "--output", "json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("missing store should still fail diagnostics")
	}
	var missing struct {
		ProjectState adoption.Status `json:"project_state"`
		HealthError  string          `json:"health_error"`
	}
	if err := json.Unmarshal(out.Bytes(), &missing); err != nil || missing.ProjectState.AdoptionState != "not_recorded" || missing.HealthError == "" {
		t.Fatalf("missing-store status: %s, %v", out.String(), err)
	}
	if _, err := os.Stat(config.StateDir(root)); !os.IsNotExist(err) {
		t.Fatalf("doctor created project state: %v", err)
	}
	runCLI(t, "", "trial", "start", "--prose", "AGENTS.md")
	for _, phase := range []string{"trial", "adopted", "exited"} {
		if phase == "adopted" {
			runCLI(t, "", "trial", "adopt", "--reviewed")
		}
		if phase == "exited" {
			s, err := adoption.Load(config.StateDir(root))
			if err != nil {
				t.Fatal(err)
			}
			s.History = append(s.History, adoption.Transition{State: "exited"})
			if err = adoption.Save(config.StateDir(root), *s); err != nil {
				t.Fatal(err)
			}
		}
		var report struct {
			ProjectState adoption.Status `json:"project_state"`
		}
		if err := json.Unmarshal([]byte(runCLI(t, "", "doctor", "--output", "json")), &report); err != nil {
			t.Fatal(err)
		}
		if report.ProjectState.AdoptionState != phase || report.ProjectState.LiveClient != "not_verified" {
			t.Fatalf("doctor state: %+v", report)
		}
		if phase == "exited" && report.ProjectState.EffectiveHooks != "disabled" {
			t.Fatal(report)
		}
	}
}

func TestTrialRunsLocalWorkflowButAuditRemainsPassive(t *testing.T) {
	testProject(t)
	runCLI(t, "", "trial", "start", "--prose", "AGENTS.md")
	runCLI(t, "", "checkpoint", "create", "--task-id", "real-work", "--task", "Implement the real workflow", "--summary", "Known state", "--next", "Verify recovery")
	if out := runCLI(t, `{"session_id":"fresh-trial","hook_event_name":"SessionStart"}`, "hook", "codex"); !strings.Contains(out, "Implement the real workflow") || !strings.Contains(out, "Full local") && !strings.Contains(out, "full local") {
		t.Fatalf("no trial recovery: %s", out)
	}
	runCLI(t, `{"session_id":"fresh-trial","hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"AGENTS.md"}}`, "hook", "codex")
	a, err := app.Open(context.Background(), mustProjectRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.DB.QueryRow(`SELECT COUNT(*) FROM actor_invocations`).Scan(&count); err != nil || count < 2 {
		t.Fatalf("trial lost normal observation: %d %v", count, err)
	}
	a.Close()
	runCLI(t, "", "mode", "audit-only")
	if out := runCLI(t, `{"session_id":"audit-trial","hook_event_name":"SessionStart"}`, "hook", "codex"); out != "" {
		t.Fatalf("audit leaked context: %s", out)
	}
	if out := runCLI(t, "", "doctor"); !strings.Contains(out, "Effective hooks: passive_usage") || !strings.Contains(out, "Adoption state: trial") {
		t.Fatal(out)
	}
	runCLI(t, "", "mode", "full")
	if out := runCLI(t, "", "bootstrap"); !strings.Contains(out, "Implement the real workflow") {
		t.Fatal(out)
	}
	requireCLIError(t, "History sync", "sync")
}

func mustProjectRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func importFixture(t *testing.T) proseimport.Bundle {
	t.Helper()
	if err := os.WriteFile("PLAN.md", []byte("# Build API\n\nImplement a parser.\ntoken=private-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Documents []proseimport.Document `json:"documents"`
	}
	if err := json.Unmarshal([]byte(runCLI(t, "", "import-prose", "--sources", "PLAN.md")), &snapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot.Documents[0].Text, "private-test-token") {
		t.Fatal("snapshot leaked secret")
	}
	return proseimport.Bundle{Version: 1, ID: "onboard-1", Creator: "test-agent", Model: "test-model", Sources: []proseimport.Source{snapshot.Documents[0].Source}, Tracks: []proseimport.Track{{ID: "api", PlanID: "api-plan", Title: "Build API", Summary: "Imported current API plan", SourcePath: "PLAN.md", Section: "Build API", Items: []trackplan.Item{{ID: "parser", Position: 1, Title: "Implement parser", Detail: "token=private-test-token", Status: "pending", AcceptanceCriteria: "Parser checks pass"}}}}}
}

func TestProseImportPreviewApplyReplayAndTUI(t *testing.T) {
	root := testProject(t)
	b := importFixture(t)
	runCLI(t, "", "trial", "start", "--prose", "PLAN.md")
	file := writeJSONFile(t, b)
	if out := runCLI(t, "", "import-prose", "--file", file); !strings.Contains(out, `"state": "preview"`) && !strings.Contains(out, `"state":"preview"`) {
		t.Fatal(out)
	}
	a, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.DB.QueryRow(`SELECT COUNT(*) FROM roadmap_nodes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview persisted: %d %v", count, err)
	}
	a.Close()
	var receipt proseimport.Receipt
	if err = json.Unmarshal([]byte(runCLI(t, "", "import-prose", "--file", file, "--apply")), &receipt); err != nil || receipt.State != "applied" {
		t.Fatalf("apply: %+v %v", receipt, err)
	}
	a, err = app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	source := appTerminalSource{app: a}
	view := newTerminalView(source)
	if err := view.load(context.Background()); err != nil || len(view.work.Items) == 0 {
		t.Fatalf("new project TUI hides imported tracks: %s, %v", view.status, err)
	}
	plans, err := source.Plans(context.Background(), "api", "")
	if err != nil || len(plans.Plans) != 1 || plans.Plans[0].Status != "proposed" {
		t.Fatalf("TUI plans: %+v %v", plans, err)
	}
	inventory, err := a.Roadmap.TrackInventory(context.Background(), a.ProjectID, roadmap.TrackInventoryOptions{Filter: "everything"})
	if err != nil || len(inventory.Items) == 0 {
		t.Fatalf("TUI tracks: %+v %v", inventory, err)
	}
	p, err := source.Plan(context.Background(), "api-plan", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Items[0].Detail, "private-test-token") || !strings.Contains(strings.Join(p.Items[0].EvidenceRefs, " "), b.Sources[0].SHA256) || p.Authority != "model_suggested" {
		t.Fatalf("import lost redaction/provenance: %+v", p)
	}
	if err = json.Unmarshal([]byte(runCLI(t, "", "import-prose", "--file", file, "--apply")), &receipt); err != nil || receipt.State != "already_applied" {
		t.Fatalf("replay %+v %v", receipt, err)
	}
	b.Tracks[0].Summary = "Changed interpretation"
	requireCLIError(t, "different bundle", "import-prose", "--file", writeJSONFile(t, b), "--apply")
	b.ID = "onboard-2"
	requireCLIError(t, "reconcile current records", "import-prose", "--file", writeJSONFile(t, b), "--apply")
	// A reviewed plan is preserved as the operative revision when reimport proposes new intent.
	runCLI(t, "", "track-plan", "record", "api-plan", "--creator", "reviewer", "--status", "active")
	b.Tracks[0].ExpectedNodeRevision = 1
	b.Tracks[0].ExpectedPlanRevision = 2
	b.Tracks[0].Items[0].Title = "Implement a better parser"
	runCLI(t, "", "import-prose", "--file", writeJSONFile(t, b), "--apply")
	effective, err := a.TrackPlans.EffectivePlan(context.Background(), a.ProjectID, "api-plan")
	if err != nil || effective.Revision != 2 {
		t.Fatalf("reimport displaced reviewed work: %+v %v", effective, err)
	}
	// Source changes invalidate even a bundle whose record baselines are current.
	b.ID = "onboard-3"
	b.Tracks[0].ExpectedPlanRevision = 3
	if err = os.WriteFile(filepath.Join(root, "PLAN.md"), []byte("# Build API\nChanged source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireCLIError(t, "source \"PLAN.md\" changed", "import-prose", "--file", writeJSONFile(t, b), "--apply")
}

func TestProseImportRollsBackWholeBatchOnInvalidPlan(t *testing.T) {
	root := testProject(t)
	b := importFixture(t)
	runCLI(t, "", "init")
	second := b.Tracks[0]
	second.ID = "second"
	second.PlanID = "second-plan"
	second.Items = []trackplan.Item{{ID: "broken", Title: "Broken", Status: "made-up"}}
	b.Tracks = append(b.Tracks, second)
	requireCLIError(t, "unsupported item status", "import-prose", "--file", writeJSONFile(t, b), "--apply")
	a, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, table := range []string{"roadmap_nodes", "track_plan_revisions", "prose_imports"} {
		var count int
		if err = a.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s rows %d: %v", table, count, err)
		}
	}
}

func TestProseSnapshotRefusesUnsafeAndOversizedSources(t *testing.T) {
	root := testProject(t)
	if err := os.WriteFile("huge.md", []byte(strings.Repeat("x", proseimport.MaxSourceBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.md", ".git/config", ".llewm/secret.md", "huge.md"} {
		if _, err := proseimport.Snapshot(root, []string{path}, proseimport.MaxTotalBytes); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if err := os.Symlink(filepath.Join(root, "AGENTS.md"), "linked.md"); err == nil {
		if _, err = proseimport.Snapshot(root, []string{"linked.md"}, 4096); err == nil {
			t.Fatal("accepted symlink")
		}
	}
}
