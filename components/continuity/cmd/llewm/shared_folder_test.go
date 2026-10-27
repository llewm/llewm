package main

import (
	"context"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedFolderCLIJoinsExistingHistoryWithoutGit(t *testing.T) {
	rootA := testProject(t)
	runCLI(t, "", "init")
	var identity portable.Identity
	decodeCLI(t, runCLI(t, "", "sync", "init"), &identity)
	runCLI(t, "", "checkpoint", "create", "--task-id", "folder-cli", "--task", "Recover shared folder work", "--next", "Verify receiving install")
	directory := t.TempDir()
	var endpoint foldersync.Endpoint
	decodeCLI(t, runCLI(t, "", "sync", "folder", "add", "personal", "--directory", directory, "--create", "--confirm-lineage", identity.LineageID), &endpoint)
	if endpoint.StoreID == "" || endpoint.Relay {
		t.Fatalf("unexpected folder config: %+v", endpoint)
	}
	if err, _ := runCLIError("sync", "folder", "convert", "personal"); err == nil || !strings.Contains(err.Error(), "--writers-stopped") {
		t.Fatalf("conversion did not require writer confirmation: %v", err)
	}
	runCLI(t, "", "sync", "folder", "convert", "personal", "--writers-stopped")
	var result historyResult
	decodeCLI(t, runCLI(t, "", "sync", "--output", "json"), &result)
	if result.Git != nil || len(result.Folders) != 1 || result.Folders[0].PeerReceipt != "unknown" || result.Folders[0].PublishedRecords == 0 {
		t.Fatalf("misleading folder-only receipt: %+v", result)
	}
	if got := runCLI(t, "", "sync", "status"); !strings.Contains(got, "published_locally") || strings.Contains(got, "last synced") {
		t.Fatalf("folder publication claimed peer sync: %s", got)
	}
	rootB := testProject(t)
	runCLI(t, "", "init")
	if err, _ := runCLIError("sync", "folder", "add", "personal", "--directory", directory, "--store-id", endpoint.StoreID, "--confirm-lineage", identity.LineageID); err == nil {
		t.Fatal("implicitly adopted source lineage")
	}
	runCLI(t, "", "sync", "init", "--lineage", identity.LineageID)
	if err, _ := runCLIError("sync", "folder", "add", "personal", "--directory", directory, "--confirm-lineage", identity.LineageID); err == nil {
		t.Fatal("did not require exact store pin")
	}
	runCLI(t, "", "sync", "folder", "add", "personal", "--directory", directory, "--store-id", endpoint.StoreID, "--confirm-lineage", identity.LineageID)
	decodeCLI(t, runCLI(t, "", "sync", "--output", "json"), &result)
	if result.Folders[0].ImportedRecords == 0 {
		t.Fatal("fresh receiver did not import source history")
	}
	if got := runCLI(t, "", "bootstrap", "--task-id", "folder-cli"); !strings.Contains(got, "Recover shared folder work") {
		t.Fatalf("source task not recovered: %s", got)
	}
	cfg, err := config.Load(rootB)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.Remote != "" || cfg.Sync.Folders[0].Relay {
		t.Fatal("folder setup added implicit Git/relay")
	}
	runCLI(t, "", "sync", "relay", "personal", "--enable=true", "--confirm-lineage", identity.LineageID)
	cfg, err = config.Load(rootB)
	if err != nil || !cfg.Sync.Folders[0].Relay {
		t.Fatal("explicit relay setting not retained")
	}
	runCLI(t, "", "sync", "folder", "remove", "personal")
	cfg, err = config.Load(rootB)
	if err != nil || cfg.Sync.Configured() {
		t.Fatal("folder endpoint not removed")
	}
	if _, err := os.Stat(filepath.Join(directory, foldersync.MarkerFile)); err != nil {
		t.Fatal("removed shared files while removing endpoint")
	}
	if _, err := os.Stat(filepath.Join(config.StateDir(rootA), "history", "lineage")); err != nil {
		t.Fatal("no durable local archive")
	}
}
func TestFolderSessionHookQueuesBackgroundWithoutReadingEndpoint(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	var id portable.Identity
	decodeCLI(t, runCLI(t, "", "sync", "init"), &id)
	directory := t.TempDir()
	marker, err := foldersync.Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sync.Folders = []foldersync.Endpoint{{Name: "missing", Directory: directory, StoreID: marker.StoreID, LineageID: id.LineageID}}
	if _, err := config.SetSync(root, cfg.Sync); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	calls := 0
	previous := startBackgroundSync
	startBackgroundSync = func(executable, project string) error { calls++; return nil }
	t.Cleanup(func() { startBackgroundSync = previous })
	got := sessionStartSync(context.Background(), application)
	if calls != 1 || !strings.Contains(got, "bootstrap uses last imported history") {
		t.Fatalf("hook did not defer folder I/O: %d %s", calls, got)
	}
	if _, err := os.Stat(filepath.Join(config.StateDir(root), "history")); !os.IsNotExist(err) {
		t.Fatal("session hook unexpectedly scanned/created local archive")
	}
	enableAutomaticSync(application)
	if application.AfterCheckpoint == nil {
		t.Fatal("folder-only checkpoints do not schedule automatic sync")
	}
	application.AfterCheckpoint()
	if calls != 2 {
		t.Fatal("checkpoint background sync not scheduled")
	}
}
func TestFolderMarkerInspectOutsideProjectAndTrialSyncGate(t *testing.T) {
	directory := t.TempDir()
	marker, err := foldersync.Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	clean := t.TempDir()
	t.Chdir(clean)
	t.Setenv("CONTINUITY_HOME", filepath.Join(clean, "state"))
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(clean, "global"))
	var inspected foldersync.Marker
	if err := json.Unmarshal([]byte(runCLI(t, "", "sync", "folder", "inspect", directory)), &inspected); err != nil || inspected != marker {
		t.Fatalf("marker inspect: %+v %v", inspected, err)
	}
	if _, err := os.Stat(filepath.Join(clean, "state")); !os.IsNotExist(err) {
		t.Fatal("inspect initialized project state")
	}
}

func TestSharedFolderSetupCannotBypassTrialSyncGate(t *testing.T) {
	testProject(t)
	runCLI(t, "", "trial", "start", "--prose", "AGENTS.md")
	directory := t.TempDir()
	for _, args := range [][]string{{"sync"}, {"sync", "folder", "add", "personal", "--directory", directory, "--create", "--confirm-lineage", "lineage:" + strings.Repeat("a", 32)}, {"sync", "relay", "personal", "--enable=true", "--confirm-lineage", "lineage:" + strings.Repeat("a", 32)}} {
		if err, _ := runCLIError(args...); err == nil || !strings.Contains(err.Error(), "trial") {
			t.Fatalf("trial sync gate bypassed: %v %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, foldersync.MarkerFile)); !os.IsNotExist(err) {
		t.Fatal("trial setup published marker before refusal")
	}
}
