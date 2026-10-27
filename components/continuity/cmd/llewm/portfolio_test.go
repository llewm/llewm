package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
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
	runCLI(t, "", "portfolio", "destination", "add", "private", "--kind", "git", "--remote", remote)
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

func TestPortfolioFolderCLIFromOutsideWorkspace(t *testing.T) {
	rootA := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init")
	rootB := testProject(t)
	runCLI(t, "", "init")
	runCLI(t, "", "sync", "init")
	t.Setenv("CONTINUITY_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var a, b portfolio.Project
	decodeCLI(t, runCLI(t, "", "portfolio", "project", "inspect", rootA), &a)
	decodeCLI(t, runCLI(t, "", "portfolio", "project", "inspect", rootB), &b)
	directory := t.TempDir()
	requireCLIError(t, "kind", "portfolio", "destination", "add", "bad", "--kind", "git", "--directory", directory, "--create")
	requireCLIError(t, "Git fields", "portfolio", "destination", "add", "bad", "--kind", "folder", "--directory", directory, "--create", "--remote", "/private/repo.git")
	if _, err := os.Stat(filepath.Join(directory, foldersync.MarkerFile)); !os.IsNotExist(err) {
		t.Fatal("invalid destination created marker")
	}
	runCLI(t, "", "portfolio", "destination", "add", "private", "--kind", "folder", "--directory", directory, "--create")
	var marker foldersync.Marker
	decodeCLI(t, runCLI(t, "", "sync", "folder", "inspect", directory), &marker)
	if marker.StoreID == "" {
		t.Fatal("missing container identity")
	}
	for _, p := range []struct {
		name  string
		value portfolio.Project
	}{{"a", a}, {"b", b}} {
		runCLI(t, "", "portfolio", "project", "add", p.name, "--root", p.value.Root, "--confirm-project", p.value.ProjectID, "--confirm-lineage", p.value.Lineage, "--destination", "private")
	}
	var preview []portfolio.Result
	decodeCLI(t, runCLI(t, "", "portfolio", "sync", "--all", "--dry-run", "--output", "json"), &preview)
	if len(preview) != 2 || preview[0].Destinations[0].Kind != "folder" || preview[1].Destinations[0].StoreID != marker.StoreID {
		t.Fatalf("wrong folder pins: %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(directory, "v4")); !os.IsNotExist(err) {
		t.Fatal("preview published objects")
	}
	requireCLIError(t, "--writers-stopped", "portfolio", "convert", "a", "private")
	runCLI(t, "", "portfolio", "convert", "a", "private", "--writers-stopped")
	runCLI(t, "", "portfolio", "convert", "b", "private", "--writers-stopped")
	runCLI(t, "", "portfolio", "sync", "--all", "--output", "json")
	decodeCLI(t, runCLI(t, "", "portfolio", "status", "--all"), &preview)
	if preview[0].Destinations[0].FolderStatus == nil || preview[1].Destinations[0].Outcome != "published_locally" {
		t.Fatalf("folder status missing: %+v", preview)
	}
	runCLI(t, "", "portfolio", "project", "remove", "a")
	runCLI(t, "", "portfolio", "project", "remove", "b")
	runCLI(t, "", "portfolio", "destination", "remove", "private")
	requireCLIError(t, "store-id", "portfolio", "destination", "add", "private", "--kind", "folder", "--directory", directory)
	runCLI(t, "", "portfolio", "destination", "add", "private", "--kind", "folder", "--directory", directory, "--store-id", marker.StoreID)
	if _, err := os.Stat(filepath.Join(directory, foldersync.MarkerFile)); err != nil {
		t.Fatal("removal deleted container")
	}
}
