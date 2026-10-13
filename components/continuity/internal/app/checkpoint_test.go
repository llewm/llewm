package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

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
