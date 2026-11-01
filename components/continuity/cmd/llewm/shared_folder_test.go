package main

import (
	"context"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitFolderCoordinatorRecoversCommittedPublicationIntent(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := testProject(t)
	runCLI(t, "", "init")
	remote := filepath.Join(t.TempDir(), "memory.git")
	if output, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("initialize remote: %v %s", err, output)
	}
	runCLI(t, "", "sync", "init", "--remote", remote)
	var status struct {
		Identity portable.Identity `json:"identity"`
	}
	decodeCLI(t, runCLI(t, "", "sync", "status", "--output", "json"), &status)
	var endpoint foldersync.Endpoint
	decodeCLI(t, runCLI(t, "", "sync", "folder", "add", "first", "--directory", t.TempDir(), "--create", "--confirm-lineage", status.Identity.LineageID), &endpoint)
	runCLI(t, "", "sync", "folder", "convert", "first", "--writers-stopped")
	application, err := app.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	archive, err := projectArchive(application)
	if err != nil {
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: status.Identity.LineageID, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "git-folder-fixture", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Index: []byte(`{"title":"Tracked decision","status":"accepted"}`), Payload: []byte(`{"title":"Tracked decision","rationale":"Preserve staged intent","disposition":"accepted","actor":"owner","actor_type":"human","review_source":"","links":[],"operational_schema_version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	index := recordsync.Index{DB: application.DB, ProjectID: application.ProjectID, Lineage: status.Identity.LineageID}
	staged, err := index.PlanPublication(ctx, archive, endpoint.ID(), false)
	if err != nil || len(staged) != 1 {
		t.Fatalf("stage folder intent: %d %v", len(staged), err)
	}
	syncer, err := projectSyncer(application)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := syncer.Session(ctx, 0); err != nil || result.ExportedRecords == 0 || result.UnpushedCommits == 0 {
		t.Fatalf("stage committed Git publication without network: %+v %v", result, err)
	}
	// Simulate loss of derived membership after object/ref publication. The
	// coordinator must recover it from committed bytes before relaying them.
	if _, err := application.DB.ExecContext(ctx, `DELETE FROM history_index_publications WHERE project_id=? AND endpoint_hash=? AND block_hash=?`, application.ProjectID, recordsync.Hash(syncer.Endpoint()), staged[0].Hash); err != nil {
		t.Fatal(err)
	}
	// Durable observations survive disposable index damage; recovery must
	// rebuild membership without deleting or overwriting the archive source.
	if _, err := application.DB.ExecContext(ctx, `UPDATE history_index_envelopes SET envelope_json='{}' WHERE project_id=? AND envelope_hash IN (SELECT envelope_hash FROM history_index_block_members WHERE block_hash=?)`, application.ProjectID, staged[0].Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := runHistorySync(ctx, application, false, false); err != nil {
		t.Fatalf("coordinated rebuild: %v", err)
	}
	// Local semantic membership is not a transport destination.
	var members int
	if err := application.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=? AND endpoint_hash<>?`, application.ProjectID, staged[0].Hash, recordsync.Hash("local-snapshot:"+status.Identity.LineageID)).Scan(&members); err != nil || members != 2 {
		t.Fatalf("publication memberships: %d %v", members, err)
	}
}

func TestFolderOnlyCoordinatorRecoversFromAnotherEndpoint(t *testing.T) {
	ctx := context.Background()
	root := testProject(t)
	runCLI(t, "", "init")
	var identity portable.Identity
	decodeCLI(t, runCLI(t, "", "sync", "init"), &identity)
	var endpoints []foldersync.Endpoint
	for _, name := range []string{"first", "second"} {
		var endpoint foldersync.Endpoint
		decodeCLI(t, runCLI(t, "", "sync", "folder", "add", name, "--directory", t.TempDir(), "--create", "--confirm-lineage", identity.LineageID), &endpoint)
		runCLI(t, "", "sync", "folder", "convert", name, "--writers-stopped")
		endpoints = append(endpoints, endpoint)
	}
	application, err := app.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	archive, err := projectArchive(application)
	if err != nil {
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: identity.LineageID, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "coordinator-fixture", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Index: []byte(`{"title":"Recovered decision","status":"accepted"}`), Payload: []byte(`{"title":"Recovered decision","rationale":"Preserve staged intent","disposition":"accepted","actor":"owner","actor_type":"human","review_source":"","links":[],"operational_schema_version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	index := recordsync.Index{DB: application.DB, ProjectID: application.ProjectID, Lineage: identity.LineageID}
	staged, err := index.PlanPublication(ctx, archive, endpoints[0].ID(), false)
	if err != nil || len(staged) != 1 {
		t.Fatalf("stage: %d %v", len(staged), err)
	}
	destination, err := recordsync.OpenRoot(endpoints[1].Directory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = recordsync.PublishBlock(destination, identity.LineageID, staged[0])
	destination.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Durable observations survive disposable index damage; recovery must
	// rebuild membership without deleting or overwriting the archive source.
	if _, err := application.DB.ExecContext(ctx, `UPDATE history_index_envelopes SET envelope_json='{}' WHERE project_id=? AND envelope_hash IN (SELECT envelope_hash FROM history_index_block_members WHERE block_hash=?)`, application.ProjectID, staged[0].Hash); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(endpoints[1].Directory, foldersync.MarkerFile)
	if err := os.Rename(markerPath, markerPath+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := runHistorySync(ctx, application, false, false); err == nil {
		t.Fatal("rebuild accepted an unavailable endpoint")
	}
	// Local semantic membership is not a transport destination.
	var members int
	if err := application.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=? AND endpoint_hash<>?`, application.ProjectID, staged[0].Hash, recordsync.Hash("local-snapshot:"+identity.LineageID)).Scan(&members); err != nil || members != 1 {
		t.Fatalf("unavailable endpoint changed staged membership: %d %v", members, err)
	}
	if err := os.Rename(markerPath+".unavailable", markerPath); err != nil {
		t.Fatal(err)
	}
	if _, err := runHistorySync(ctx, application, false, false); err != nil {
		t.Fatalf("coordinated rebuild: %v", err)
	}
	if err := application.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=? AND endpoint_hash<>?`, application.ProjectID, staged[0].Hash, recordsync.Hash("local-snapshot:"+identity.LineageID)).Scan(&members); err != nil || members != 2 {
		t.Fatalf("staged and observed memberships: %d %v", members, err)
	}
}

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
	source, err := app.OpenReadOnly(context.Background(), rootA)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	archive := recordsync.Archive{DB: source.DB, ProjectID: source.ProjectID, Lineage: identity.LineageID}
	objects, err := archive.Objects(context.Background(), "", true)
	if err != nil || len(objects) == 0 {
		t.Fatalf("durable observations missing after endpoint removal: %d %v", len(objects), err)
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
	var archiveStatus string
	var archiveRecords int
	if err := application.DB.QueryRow(`SELECT status,record_count FROM history_archive_state WHERE project_id=?`, application.ProjectID).Scan(&archiveStatus, &archiveRecords); err != nil || archiveStatus != "staging" || archiveRecords != 0 {
		t.Fatalf("session hook synchronously opened/captured SQLite archive: %s %d %v", archiveStatus, archiveRecords, err)
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
