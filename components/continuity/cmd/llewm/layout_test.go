package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/config"
)

func TestLayoutCLIPreservesProjectLineageAndCheckpoint(t *testing.T) {
	root := testProject(t)
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".continuity"))
	runCLI(t, "", "init")
	before := projectShow(t)
	identity := runCLI(t, "", "sync", "init")
	runCLI(t, "", "checkpoint", "create", "--task-id", "preserved", "--task", "Preserve live work", "--summary", "Learning must survive the move", "--next", "Continue from this state")
	t.Setenv("CONTINUITY_HOME", "")
	requireCLIError(t, "legacy .continuity", "init")
	preview := runCLI(t, "", "project", "migrate-layout")
	if !strings.Contains(preview, "destination") {
		t.Fatal(preview)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	runCLI(t, "", "project", "migrate-layout", "--apply", "--writers-stopped", "--backup", backup)
	after := projectShow(t)
	if after.ProjectID != before.ProjectID {
		t.Fatal("project identity changed")
	}
	if out := runCLI(t, "", "sync", "init"); out != identity {
		t.Fatalf("lineage identity changed: %s / %s", identity, out)
	}
	if out := runCLI(t, "", "checkpoint", "show", "--query", "Learning must survive"); !strings.Contains(out, "Preserve live work") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(root, ".llewm", "project.toml")); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "", "checkpoint", "create", "--task-id", "new-work", "--task", "New work after move", "--next", "Keep new knowledge")
	requireCLIError(t, "new state changed", "project", "restore-layout", "--writers-stopped", "--backup", backup)
	if _, err := os.Stat(config.Default(root).Storage.Database); err != nil {
		t.Fatal("new store lost")
	}
}
func TestNewProjectInitWritesUnifiedLayout(t *testing.T) {
	root := testProject(t)
	t.Setenv("CONTINUITY_HOME", "")
	runCLI(t, "", "init")
	runCLI(t, "", "init")
	for _, path := range []string{".llewm/project.toml", ".llewm/continuity/config.toml", ".llewm/continuity/continuity.db"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".continuity")); !os.IsNotExist(err) {
		t.Fatal("legacy state created")
	}
}
