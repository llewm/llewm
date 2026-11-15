package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func TestSaveCheckpointFitsLargeProjectGuidanceLikeStartup(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, notices := range []bool{false, true} {
			t.Run(fmt.Sprintf("global_%t/notices_%t", global, notices), func(t *testing.T) {
				ctx := context.Background()
				root, globalRoot := t.TempDir(), t.TempDir()
				source := filepath.Join(root, "CLAUDE.md")
				if err := os.WriteFile(source, []byte("# Large instructions\n"+strings.Repeat("x", 25000)), 0600); err != nil {
					t.Fatal(err)
				}
				if global {
					if err := os.WriteFile(filepath.Join(globalRoot, "AGENTS.md"), []byte("# Global\nRespect project overrides."), 0600); err != nil {
						t.Fatal(err)
					}
				}
				db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01')`, root); err != nil {
					t.Fatal(err)
				}
				service := checkpoint.Service{DB: db}
				prior, err := service.Create(ctx, checkpoint.Checkpoint{ProjectID: "p", TaskID: "task", Task: "Prior", NextAction: "Keep this unresolved work"})
				if err != nil {
					t.Fatal(err)
				}
				compiler := bootstrap.Compiler{Checkpoints: service, GlobalRoot: globalRoot, MaxBytes: 24576}
				if notices {
					compiler.GrantCheck = func(context.Context, string) string { return "Existing grant notice" }
					compiler.Choose = func(_ context.Context, _, _ string, selection checkpoint.Selection) *bootstrap.Choice {
						if selection != (checkpoint.Selection{}) {
							return &bootstrap.Choice{Selection: selection, Rule: bootstrap.RuleExplicit}
						}
						return &bootstrap.Choice{Rule: bootstrap.RuleBranch, Detail: "its latest checkpoint was on main"}
					}
				}
				candidate := checkpoint.Checkpoint{ID: "candidate", ProjectID: "p", TaskID: "task", PredecessorCheckpointID: prior.ID, Task: "Small handoff", Summary: "Ready", NextAction: "Continue", Completed: []string{strings.Repeat("done ", 400)}, Attempts: []checkpoint.Attempt{{Description: "Failed approach", Outcome: "Do not repeat"}}}
				ready, err := compiler.Preflight(ctx, "p", root, "test", candidate)
				if err != nil {
					t.Fatal(err)
				}
				application := &App{Root: root, ProjectID: "p", Config: config.Config{Project: config.Project{Name: "test"}}, Checkpoints: service, Bootstrap: compiler}
				created, saveErr := application.SaveCheckpoint(ctx, candidate)
				if global {
					if ready.Ready || saveErr == nil || len(ready.DeferredRules) != 0 {
						t.Fatalf("discarded project overrides: %+v %v", ready, saveErr)
					}
					for _, want := range []string{source, "25000", "global guidance", "bootstrap.max_bytes"} {
						if !strings.Contains(ready.BudgetError, want) || !strings.Contains(saveErr.Error(), want) {
							t.Fatalf("missing %q in diagnostics: %+v %v", want, ready, saveErr)
						}
					}
					var count int
					if err := db.QueryRow(`SELECT count(*) FROM checkpoints`).Scan(&count); err != nil || count != 1 {
						t.Fatalf("failed save persisted: %d %v", count, err)
					}
					return
				}
				if !ready.Ready || saveErr != nil || len(ready.DeferredRules) != 1 || ready.DeferredRules[0].Source != source || len(ready.DeferredCheckpointFields) == 0 {
					t.Fatalf("fit/save: %+v %v", ready, saveErr)
				}
				if len(created.Checkpoint.Completed) != 1 || len(created.Checkpoint.Attempts) != 1 {
					t.Fatal("save lost full detail")
				}
				for _, selection := range []checkpoint.Selection{{}, {CheckpointID: created.Checkpoint.ID}} {
					packet, err := compiler.BuildSelected(ctx, "p", root, "test", selection)
					if err != nil {
						t.Fatal(err)
					}
					body, err := json.MarshalIndent(packet, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if len(body)+1 > ready.JSONBytes || len(bootstrap.Markdown(packet)) > ready.MarkdownBytes {
						t.Fatalf("readback exceeded preflight: %d %d vs %+v", len(body)+1, len(bootstrap.Markdown(packet)), ready)
					}
					if len(packet.UnresolvedActions) != 1 || len(packet.Checkpoint.Attempts) != 1 || !strings.Contains(bootstrap.Markdown(packet), source) {
						t.Fatal("missing protected state or deferred source")
					}
				}
			})
		}
	}
}

func TestSaveCheckpointRejectsOversizedBootstrapWithoutWrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	globalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, "AGENTS.md"), []byte("# Global\n\nKeep commits focused."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	application := &App{
		Root: root, ProjectID: "p", Config: config.Config{Project: config.Project{Name: "test"}},
		Checkpoints: checkpoints,
		Bootstrap:   bootstrap.Compiler{Checkpoints: checkpoints, MaxBytes: 900, GlobalRoot: globalRoot},
	}

	_, err = application.SaveCheckpoint(ctx, checkpoint.Checkpoint{
		TaskID: "bounded", Task: "Bounded task", Summary: strings.Repeat("detail ", 300), NextAction: "Continue in a fresh session",
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoint bootstrap exceeds 900-byte budget") {
		t.Fatalf("unexpected oversized save result: %v", err)
	}
	for _, table := range []string{"checkpoints", "checkpoint_actions", "checkpoint_action_transitions"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("oversized save wrote %d rows to %s", count, table)
		}
	}
}

func TestSaveCheckpointPersistsReadyCandidate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	application := &App{
		Root: root, ProjectID: "p", Config: config.Config{Project: config.Project{Name: "test"}},
		Checkpoints: checkpoints,
		Bootstrap:   bootstrap.Compiler{Checkpoints: checkpoints, MaxBytes: 4_000, GlobalRoot: t.TempDir()},
	}

	created, err := application.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "bounded", Task: "Bounded task", Summary: "Ready", NextAction: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Checkpoint.ID == "" {
		t.Fatal("saved checkpoint has no ID")
	}
	for table, want := range map[string]int{"checkpoints": 1, "checkpoint_actions": 1, "checkpoint_action_transitions": 1} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows=%d want=%d", table, count, want)
		}
	}
}

func TestSaveCompletedCheckpointEnforcesAtomicActionReconciliation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Rules\n\nRun tests."), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p',?,'test','2026-01-01T00:00:00Z')`, root); err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpoint.Service{DB: db}
	application := &App{
		Root: root, ProjectID: "p", Config: config.Config{Project: config.Project{Name: "test"}},
		Checkpoints: checkpoints,
		Bootstrap:   bootstrap.Compiler{Checkpoints: checkpoints, MaxBytes: 5_000, GlobalRoot: t.TempDir()},
	}
	if _, err := application.SaveCheckpoint(ctx, checkpoint.Checkpoint{TaskID: "task", Task: "Task", NextAction: "Finish task"}); err != nil {
		t.Fatal(err)
	}
	actions, err := checkpoints.Unresolved(ctx, "p", "task", "")
	if err != nil || len(actions) != 1 {
		t.Fatalf("unexpected predecessor actions: %#v %v", actions, err)
	}
	terminal := checkpoint.Checkpoint{TaskID: "task", PredecessorCheckpointID: actions[0].SourceCheckpointID, Task: "Task", Status: "completed", Summary: "Done", NextAction: "Task complete; await the next objective."}
	if _, err := application.SaveCheckpoint(ctx, terminal); err == nil || !strings.Contains(err.Error(), "must reconcile or explicitly carry forward") {
		t.Fatalf("completed save bypassed action reconciliation: %v", err)
	}
	reconciliation := checkpoint.ActionReconciliation{Updates: []checkpoint.ActionUpdate{{ID: actions[0].ID, Status: "completed", Reason: "done"}}}
	readiness, err := application.Bootstrap.PreflightWithReconciliation(ctx, "p", root, "test", terminal, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.CurrentUnresolvedActionCount != 1 || readiness.ProjectedUnresolvedActionCount != 0 || readiness.ReconciledActionCount != 1 || readiness.CreatesNextAction || len(readiness.ProjectedPredecessorActions) != 0 {
		t.Fatalf("unexpected projected lifecycle: %#v", readiness)
	}
	created, err := application.SaveCheckpointWithReconciliation(ctx, terminal, reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if created.ActionCreated || len(created.Reconciled) != 1 {
		t.Fatalf("unexpected terminal result: %#v", created)
	}
}
