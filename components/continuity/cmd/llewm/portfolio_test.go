package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/portfolio"
)

func TestPortfolioCLIWorksWithoutWorkspaceAndPreservesState(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init")
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var p portfolio.Project
	decodeCLI(t, runCLI(t, "", "portfolio", "project", "inspect", root), &p)
	if p.ProjectID == "" || p.Lineage == "" {
		t.Fatal("inspect lost source identity")
	}
	empty := t.TempDir()
	t.Chdir(empty)
	runCLI(t, "", "portfolio", "show")
	global, _ := config.GlobalDir()
	if _, err := os.Stat(filepath.Join(global, portfolio.FileName)); !os.IsNotExist(err) {
		t.Fatal("show created catalog")
	}
	remote := filepath.Join(t.TempDir(), "uncontacted.git")
	runCLI(t, "", "portfolio", "destination", "add", "private", "--remote", remote)
	requireCLIError(t, "must match", "portfolio", "project", "add", "a", "--root", root, "--confirm-project", "project-wrong", "--confirm-lineage", p.Lineage, "--destination", "private")
	runCLI(t, "", "portfolio", "project", "add", "a", "--root", root, "--confirm-project", p.ProjectID, "--confirm-lineage", p.Lineage, "--destination", "private")
	requireCLIError(t, "confirmed", "portfolio", "relay", "a", "private", "--enable=true", "--confirm-lineage", "wrong")
	requireCLIError(t, "still routed", "portfolio", "destination", "remove", "private")
	requireCLIError(t, "select one", "portfolio", "sync")
	requireCLIError(t, "select one", "portfolio", "sync", "a", "--all")
	var preview []portfolio.Result
	decodeCLI(t, runCLI(t, "", "portfolio", "sync", "a", "--dry-run", "--output", "json"), &preview)
	if len(preview) != 1 || preview[0].Outcome != "preview" || preview[0].Destinations[0].Relay {
		t.Fatalf("incorrect preview: %+v", preview)
	}
	decodeCLI(t, runCLI(t, "", "portfolio", "status", "a"), &preview)
	if preview[0].Destinations[0].Outcome != "never_run" {
		t.Fatal("status invented successful sync")
	}
	clone, _ := config.DefaultMemoryClone(remote)
	for _, absent := range []string{remote, clone, filepath.Join(empty, ".llewm"), filepath.Join(config.StateDir(root), "history")} {
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Fatalf("configuration/preview created %s: %v", absent, err)
		}
	}
	runCLI(t, "", "portfolio", "relay", "a", "private", "--enable=true", "--confirm-lineage", p.Lineage)
	var catalog portfolio.Catalog
	decodeCLI(t, runCLI(t, "", "portfolio", "show"), &catalog)
	if !catalog.Projects[0].Routes[0].Relay {
		t.Fatal("explicit route relay not saved")
	}
	runCLI(t, "", "portfolio", "project", "remove", "a")
	runCLI(t, "", "portfolio", "destination", "remove", "private")
	if _, err := os.Stat(config.Default(root).Storage.Database); err != nil {
		t.Fatal("configuration removal deleted source state")
	}
}

func TestPortfolioCLIRefusesMissingSourceWithoutInitializing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("CONTINUITY_GLOBAL_HOME", t.TempDir())
	t.Chdir(root)
	if err, _ := runCLIError("portfolio", "project", "inspect", root); err == nil {
		t.Fatal("accepted unsupported non-Git folder")
	}
	if _, err := os.Stat(filepath.Join(root, ".llewm")); !os.IsNotExist(err) {
		t.Fatal("inspect initialized source")
	}
}
