package foldersync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestBlockFolderCycleRetainsDependenciesAndRejectsIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, directory, marker.StoreID), receiver(t, directory, marker.StoreID)
	if err := source.convertV3(ctx); err != nil {
		t.Fatal(err)
	}
	objects := exportCheckpoint(t, source)
	if err := source.Archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	if result, err := source.syncBlocks(ctx); err != nil || result.PublishedRecords != len(objects) {
		t.Fatalf("source publication: %+v %v", result, err)
	}
	if result, err := target.syncBlocks(ctx); err != nil || result.ImportedRecords != len(objects) {
		t.Fatalf("target import: %+v %v", result, err)
	}
	export, err := portable.EncodeMemoryExport(ctx, target.DB, target.ProjectID)
	if err != nil || len(export.Envelopes) != len(objects) {
		t.Fatalf("target export: %d %v", len(export.Envelopes), err)
	}
	base, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, base, recordsync.Hash("partial")+".block"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := target.syncBlocks(ctx); err == nil || result.Outcome != "pending" || result.PendingFiles != 1 {
		t.Fatalf("incomplete inventory accepted: %+v %v", result, err)
	}
}

func TestFolderConversionWaitsForAllReplicatedBlocks(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, directory, marker.StoreID), receiver(t, directory, marker.StoreID)
	objects := exportCheckpoint(t, source)
	publish(t, directory, objects)
	if err := target.convertV3(ctx); err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if _, err := os.Stat(filepath.Join(directory, portable.MemoryRecordPath(object.Envelope))); err != nil {
			t.Fatalf("v3 recovery copy missing: %v", err)
		}
	}
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := recordsync.ScanBlocks(ctx, root, testLineage)
	root.Close()
	if err != nil || len(scan.Blocks) != 1 {
		t.Fatalf("converted blocks: %d %v", len(scan.Blocks), err)
	}
	dir, _ := recordsync.BlockDirectory(testLineage)
	path := filepath.Join(directory, dir, scan.Blocks[0].Hash+".block")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if result, err := target.syncBlocks(ctx); err == nil || result.Outcome != "pending" {
		t.Fatalf("incomplete conversion accepted: %+v %v", result, err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := target.syncBlocks(ctx); err != nil || result.ImportedRecords != len(objects) {
		t.Fatalf("complete conversion not imported: %+v %v", result, err)
	}
}
