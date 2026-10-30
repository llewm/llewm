package foldersync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestFolderManifestSegmentsRetainLateArrivalsAndRecoverOnFreshReceiver(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	source := receiver(t, directory, marker.StoreID)
	if err := source.Convert(ctx); err != nil {
		t.Fatal(err)
	}
	objects := exportCheckpoint(t, source)
	if err := source.Archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := recordsync.DiscoverManifestCatalog(ctx, root, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the new decision: publication follows arrival, not event time.
	_, err = (decision.Service{DB: source.DB, Now: func() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) }}).Append(ctx, decision.Record{ProjectID: source.ProjectID, ID: "backdated", Title: "Late decision", Rationale: "Arrives after newer history", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(ctx, source.DB, source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Archive.CaptureLocal(exported.Envelopes); err != nil {
		t.Fatal(err)
	}
	if result, err := source.Sync(ctx); err != nil || result.PublishedRecords == 0 {
		t.Fatalf("late publication: %+v %v", result, err)
	}
	after, err := recordsync.DiscoverManifestCatalog(ctx, root, testLineage)
	if err != nil || len(after.Roots) != len(before.Roots)+1 {
		t.Fatalf("segment roots: %+v %v", after, err)
	}
	for _, hash := range before.Roots {
		if _, err := recordsync.ManifestBlocks(ctx, root, testLineage, hash); err != nil {
			t.Fatal(err)
		}
	}
	oldBlocks := make(map[string]bool)
	for _, hash := range before.Blocks {
		oldBlocks[hash] = true
	}
	for _, hash := range after.Roots {
		if sort.SearchStrings(before.Roots, hash) < len(before.Roots) && before.Roots[sort.SearchStrings(before.Roots, hash)] == hash {
			continue
		}
		blocks, err := recordsync.ManifestBlocks(ctx, root, testLineage, hash)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range blocks {
			if oldBlocks[block] {
				t.Fatal("late segment repacked earlier block membership")
			}
		}
	}
	// A fresh receiver has no private catalog/cache and imports the same union.
	target := receiver(t, directory, marker.StoreID)
	if result, err := target.Sync(ctx); err != nil || result.ImportedRecords != len(exported.Envelopes) {
		t.Fatalf("fresh recovery: %+v %v", result, err)
	}
	scan, err := recordsync.ScanBlocks(ctx, root, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, block := range scan.Blocks {
		hashes = append(hashes, block.Hash)
	}
	if !reflect.DeepEqual(after.Blocks, hashes) {
		t.Fatal("catalog differs from complete block scan")
	}
}

func TestBlockFolderCycleRetainsDependenciesAndRejectsIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, directory, marker.StoreID), receiver(t, directory, marker.StoreID)
	if err := source.Convert(ctx); err != nil {
		t.Fatal(err)
	}
	objects := exportCheckpoint(t, source)
	if err := source.Archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	if result, err := source.Sync(ctx); err != nil || result.PublishedRecords != len(objects) {
		t.Fatalf("source publication: %+v %v", result, err)
	}
	// A successful publication announces a readable immutable catalog whose
	// block membership matches an independent complete endpoint scan.
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	scan, err := recordsync.ScanBlocks(ctx, root, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, block := range scan.Blocks {
		hashes = append(hashes, block.Hash)
	}
	nodes, err := recordsync.BuildManifest(testLineage, hashes)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := recordsync.ManifestBlocks(ctx, root, testLineage, nodes[len(nodes)-1].Hash)
	if err != nil || len(manifest) != len(hashes) {
		t.Fatalf("published catalog incomplete: %v", err)
	}
	if _, err := source.DB.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=?`, source.ProjectID); err != nil {
		t.Fatal(err)
	}
	if inventory, err := source.Inventory(ctx); err != nil || len(inventory) == 0 {
		t.Fatalf("independent inventory: %d %v", len(inventory), err)
	}
	if result, err := target.Sync(ctx); err != nil || result.ImportedRecords != len(objects) {
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
	if result, err := target.Sync(ctx); err == nil || result.Outcome != "pending" || result.PendingFiles != 1 {
		t.Fatalf("incomplete inventory accepted: %+v %v", result, err)
	}
	if inventory, err := source.Inventory(ctx); err == nil || inventory != nil {
		t.Fatalf("rebuild accepted incomplete inventory: %d %v", len(inventory), err)
	}
}

func TestFolderSyncRepairsStagedInventoryFromArchive(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	source, target := receiver(t, directory, marker.StoreID), receiver(t, directory, marker.StoreID)
	if err := source.Convert(ctx); err != nil {
		t.Fatal(err)
	}
	objects := exportCheckpoint(t, source)
	if err := source.Archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	index := recordsync.Index{DB: source.DB, ProjectID: source.ProjectID, Lineage: testLineage}
	staged, err := index.PlanPublication(ctx, source.Archive, source.Endpoint.ID(), false)
	if err != nil || len(staged) == 0 {
		t.Fatalf("stage publication: %d %v", len(staged), err)
	}
	if _, err := source.DB.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=? AND block_hash=? AND envelope_hash=?`, source.ProjectID, staged[0].Hash, staged[0].Objects[0].Envelope.EnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	if result, err := source.Sync(ctx); err != nil || result.PublishedRecords != len(objects) {
		t.Fatalf("source did not recover staged block: %+v %v", result, err)
	}
	if result, err := target.Sync(ctx); err != nil || result.ImportedRecords != len(objects) {
		t.Fatalf("peer did not recover published history: %+v %v", result, err)
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
	publishV3(t, directory, objects)
	if err := target.Convert(ctx); err != nil {
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
	if result, err := target.Sync(ctx); err == nil || result.Outcome != "pending" {
		t.Fatalf("incomplete conversion accepted: %+v %v", result, err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := target.Sync(ctx); err != nil || result.ImportedRecords != len(objects) {
		t.Fatalf("complete conversion not imported: %+v %v", result, err)
	}
}
