package foldersync

import (
	"context"
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLineage = "lineage:abababababababababababababababab"

func receiver(t *testing.T, directory, storeID string) Syncer {
	t.Helper()
	ctx := context.Background()
	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := (activity.Store{DB: db}).EnsureProject(ctx, t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: db}).Initialize(ctx, id, testLineage); err != nil {
		t.Fatal(err)
	}
	archive, err := recordsync.OpenArchive(t.TempDir(), testLineage)
	if err != nil {
		t.Fatal(err)
	}
	return Syncer{DB: db, ProjectID: id, Archive: archive, Endpoint: Endpoint{Name: "fixture", Directory: directory, StoreID: storeID}}
}
func exportCheckpoint(t *testing.T, s Syncer) []portable.EncodedEnvelope {
	t.Helper()
	_, err := (checkpoint.Service{DB: s.DB}).CreateWithActions(context.Background(), checkpoint.Checkpoint{ProjectID: s.ProjectID, TaskID: "fixture", Task: "Checkpoint dependency fixture", Summary: "Keep original attribution", NextAction: "Recover exactly", Status: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := portable.EncodeMemoryExport(context.Background(), s.DB, s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	return result.Envelopes
}
func publish(t *testing.T, directory string, objects []portable.EncodedEnvelope) {
	t.Helper()
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, e := range objects {
		if _, err := recordsync.WriteFile(root, portable.MemoryRecordPath(e.Envelope), portable.MemoryFileBytes(e), false); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOutOfOrderDependenciesAreRetainedAndRetriedTransactionally(t *testing.T) {
	directory := t.TempDir()
	marker, err := Create(directory, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, t.TempDir(), marker.StoreID), receiver(t, directory, marker.StoreID)
	all := exportCheckpoint(t, source)
	var before, after []portable.EncodedEnvelope
	for _, e := range all {
		if e.Envelope.RecordType == "checkpoint" {
			after = append(after, e)
		} else {
			before = append(before, e)
		}
	}
	if len(before) == 0 || len(after) != 1 {
		t.Fatalf("unexpected checkpoint fixture: %d/%d", len(before), len(after))
	}
	publish(t, directory, before)
	result, err := target.Sync(context.Background())
	if err == nil || result.Outcome != "pending" {
		t.Fatalf("missing dependency was not pending: %+v %v", result, err)
	}
	var count int
	if err := target.DB.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE project_id=?`, target.ProjectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial checkpoint import: %d %v", count, err)
	}
	retained, err := target.Archive.Objects(context.Background(), "", true)
	if err != nil || len(retained) != len(before) {
		t.Fatal("lost pending objects")
	}
	publish(t, directory, after)
	result, err = target.Sync(context.Background())
	if err != nil || result.ImportedRecords != len(all) {
		t.Fatalf("completed dependency set not recovered: %+v %v", result, err)
	}
	result, err = target.Sync(context.Background())
	if err != nil || result.ImportedRecords != 0 {
		t.Fatalf("replay appended semantic records: %+v %v", result, err)
	}
}
func TestUnavailableOrChangedFolderNeverReinitializesOrDeletesHistory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "folder")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker, err := Create(directory, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	target := receiver(t, directory, marker.StoreID)
	objects := exportCheckpoint(t, target)
	if err := target.Archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	detached := directory + "-detached"
	if err := os.Rename(directory, detached); err != nil {
		t.Fatal(err)
	}
	result, err := target.Sync(context.Background())
	if err == nil || result.Outcome != "unavailable" {
		t.Fatalf("missing mount accepted: %+v %v", result, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("recreated missing folder: %v", err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := Create(directory, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	if other.StoreID == marker.StoreID {
		t.Fatal("nonunique marker")
	}
	if _, err := target.Sync(context.Background()); err == nil {
		t.Fatal("replacement store silently adopted")
	}
	if _, err := os.Stat(filepath.Join(directory, "v3")); !os.IsNotExist(err) {
		t.Fatal("published into replacement store")
	}
	retained, err := target.Archive.Objects(context.Background(), "", true)
	if err != nil || len(retained) != len(objects) {
		t.Fatal("lost retained history while unavailable")
	}
}
func TestPartialConflictAndCorruptFilesCannotBecomeRecords(t *testing.T) {
	directory := t.TempDir()
	marker, err := Create(directory, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, t.TempDir(), marker.StoreID), receiver(t, directory, marker.StoreID)
	objects := exportCheckpoint(t, source)
	e := objects[0]
	path := filepath.Join(directory, portable.MemoryRecordPath(e.Envelope))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, e.Bytes[:len(e.Bytes)/2], 0600); err != nil {
		t.Fatal(err)
	}
	conflict := strings.TrimSuffix(path, ".json") + ".sync-conflict-example.json"
	if err := os.WriteFile(conflict, portable.MemoryFileBytes(e), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := target.Sync(context.Background())
	if err != nil || result.PendingFiles != 1 || result.InvalidFiles != 1 || result.ImportedRecords != 0 || result.Outcome != "pending" {
		t.Fatalf("partial/conflict files accepted: %+v %v", result, err)
	}
	// The provider finishes the canonical file later; the malformed conflict copy
	// remains visible and never substitutes for the canonical path.
	if err := os.WriteFile(path, portable.MemoryFileBytes(e), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}
	publish(t, directory, objects)
	result, err = target.Sync(context.Background())
	if err != nil || result.ImportedRecords != len(objects) {
		t.Fatalf("completed transfer not imported: %+v %v", result, err)
	}
	// Later corruption at the same content path cannot overwrite the local archive.
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Sync(context.Background()); err == nil {
		t.Fatal("overwrote or silently accepted corrupt existing object")
	}
	root, err := recordsync.OpenRoot(target.Archive.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := recordsync.ReadObject(root, portable.MemoryRecordPath(e.Envelope), testLineage); err != nil {
		t.Fatalf("local verified copy corrupted: %v", err)
	}
}
func TestSharedFolderRefusesProtectedStorage(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Separate(root, private); err == nil {
		t.Fatal("ancestor of live state accepted")
	}
	child := filepath.Join(private, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Separate(child, private); err == nil {
		t.Fatal("child of live state accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	if err := Separate(alias, private); err == nil {
		t.Fatal("symlink alias to live state accepted")
	}
	if err := Separate(t.TempDir(), private); err != nil {
		t.Fatal(err)
	}
}
