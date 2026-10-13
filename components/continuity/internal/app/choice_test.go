package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
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
