package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

func TestTaskUnavailableForImplicitSelectionUsesCurrentRoadmapState(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, "/project", "project")
	if err != nil {
		t.Fatal(err)
	}
	roadmaps := roadmap.Service{DB: db}
	if _, err := roadmaps.AppendNode(ctx, roadmap.Node{ProjectID: projectID, ID: "legacy-task", Title: "Legacy", Status: "completed", SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (checkpoint.Service{DB: db}).Create(ctx, checkpoint.Checkpoint{ProjectID: projectID, TaskID: "legacy-task", Task: "Legacy", Status: "paused", NextAction: "Old next action"}); err != nil {
		t.Fatal(err)
	}
	unavailable, err := taskUnavailableForImplicitSelection(ctx, db, projectID, "legacy-task")
	if err != nil || !unavailable {
		t.Fatalf("completed roadmap task remained implicitly selectable: unavailable=%t err=%v", unavailable, err)
	}
	if _, err := roadmaps.AppendNode(ctx, roadmap.Node{ProjectID: projectID, ID: "legacy-task", Revision: 2, Title: "Legacy", Status: "active", SourcePath: "roadmap.md"}); err != nil {
		t.Fatal(err)
	}
	unavailable, err = taskUnavailableForImplicitSelection(ctx, db, projectID, "legacy-task")
	if err != nil || unavailable {
		t.Fatalf("reactivated roadmap task stayed unavailable: unavailable=%t err=%v", unavailable, err)
	}
}

func TestRestartHandoffPersistsAndClearsAtNextTip(t *testing.T) {
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := (activity.Store{DB: db}).EnsureProject(ctx, "/project", "project")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO project_memory_identities(project_id,lineage_id,instance_id,association_source,authority,associated_at,schema_version) VALUES (?, 'lineage','device','generated','caller_asserted','2026-01-01T00:00:00Z',1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ id, client string }{{"old", "codex"}, {"new", "codex"}, {"other-client", "claude-code"}} {
		_, err = db.ExecContext(ctx, `INSERT INTO sessions(id,project_id,client,started_at,status) VALUES (?,?,?,'2026-01-01T00:00:00Z','active')`, s.id, id, s.client)
		if err != nil {
			t.Fatal(err)
		}
	}
	a := &App{DB: db, ProjectID: id}
	value := checkpoint.Checkpoint{ProjectID: id, TaskID: "restart", SessionID: "old", Task: "Restart", NextAction: "Verify restart"}
	if err := a.MarkRestartResume(ctx, &value); err != nil {
		t.Fatal(err)
	}
	service := checkpoint.Service{DB: db}
	first, err := service.Create(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(ctx, checkpoint.Checkpoint{ProjectID: id, TaskID: "other", Task: "Other", NextAction: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	selectFor := func(session string) *bootstrap.Choice {
		return chooseTask(ctx, db, project.Repositories{}, id, session, checkpoint.Selection{}, time.Now().UTC())
	}
	if got := selectFor("new"); got == nil || got.Rule != bootstrap.RuleRestart || got.Selection.TaskID != "restart" {
		t.Fatalf("matching client: %+v", got)
	}
	if got := selectFor("other-client"); got == nil || got.Rule != bootstrap.RuleAmbiguous {
		t.Fatalf("other client: %+v", got)
	}
	_, err = service.Create(ctx, checkpoint.Checkpoint{ProjectID: id, TaskID: "restart", PredecessorCheckpointID: first.ID, SessionID: "new", Task: "Restart", NextAction: "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	if got := selectFor("other-client"); got == nil || got.Rule != bootstrap.RuleAmbiguous {
		t.Fatalf("marker not cleared: %+v", got)
	}
}
