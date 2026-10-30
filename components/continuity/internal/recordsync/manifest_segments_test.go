package recordsync

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestManifestSegmentsAppendBoundedArrivals(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hashes := manifestHashes(3*MaxManifestEntries + 7)
	first, err := (BlockScanResult{}).PublishManifestSegments(ctx, root, lineage, hashes)
	if err != nil || len(first) != 4 {
		t.Fatalf("initial segments: %v %v", first, err)
	}
	for _, hash := range first {
		blocks, err := ManifestBlocks(ctx, root, lineage, hash)
		if err != nil || len(blocks) > MaxManifestEntries {
			t.Fatalf("segment bound: %d %v", len(blocks), err)
		}
	}
	prior, err := DiscoverManifestCatalog(ctx, root, lineage)
	if err != nil {
		t.Fatal(err)
	}
	scan := BlockScanResult{publicationBase: &manifestPublicationBase{root: root, lineage: lineage, blocks: prior.Blocks}}
	// A late arrival sorts before old membership. It must append a singleton,
	// never repartition any earlier full or partial segment.
	late := Hash("late-arrival")
	all := append(append([]string(nil), hashes...), late)
	added, err := scan.PublishManifestSegments(ctx, root, lineage, all)
	if err != nil || len(added) != 1 {
		t.Fatalf("arrival: %v %v", added, err)
	}
	got, err := ManifestBlocks(ctx, root, lineage, added[0])
	if err != nil || !reflect.DeepEqual(got, []string{late}) {
		t.Fatalf("repacked history: %v %v", got, err)
	}
	catalog, err := DiscoverManifestCatalog(ctx, root, lineage)
	sort.Strings(all)
	if err != nil || !reflect.DeepEqual(catalog.Blocks, all) || len(catalog.Roots) != len(first)+1 {
		t.Fatalf("union differs: %+v %v", catalog, err)
	}
	for _, old := range first {
		if _, err := ManifestBlocks(ctx, root, lineage, old); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening and losing all in-memory state recovers exactly the same union.
	reopened, err := OpenRoot(root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if roots, err := (BlockScanResult{}).PublishManifestSegments(ctx, reopened, lineage, all); err != nil || len(roots) != 0 {
		t.Fatalf("restart repacked: %v %v", roots, err)
	}
	// Reusing a scan with another destination cannot suppress its publication.
	other, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if roots, err := scan.PublishManifestSegments(ctx, other, lineage, hashes); err != nil || len(roots) != 4 {
		t.Fatalf("cross-root reuse: %v %v", roots, err)
	}
}

func TestManifestSegmentsRecoverPartialAndConcurrentPublication(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	all := manifestHashes(2*MaxManifestEntries + 3)
	sort.Strings(all)
	// Simulate a process dying after its first segment's announcement.
	first, _, err := PublishManifest(ctx, root, lineage, all[:MaxManifestEntries])
	if err != nil {
		t.Fatal(err)
	}
	if roots, err := (BlockScanResult{}).PublishManifestSegments(ctx, root, lineage, all); err != nil || len(roots) != 2 {
		t.Fatalf("partial retry: %v %v", roots, err)
	}
	before, err := DiscoverManifestCatalog(ctx, root, lineage)
	if err != nil {
		t.Fatal(err)
	}
	scan := BlockScanResult{publicationBase: &manifestPublicationBase{root: root, lineage: lineage, blocks: before.Blocks}}
	a, b := Hash("independent-a"), Hash("independent-b")
	for _, arrival := range []string{a, b} {
		if _, err := scan.PublishManifestSegments(ctx, root, lineage, append(append([]string(nil), all...), arrival)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DiscoverManifestCatalog(ctx, root, lineage)
	want := append(append([]string(nil), all...), a, b)
	sort.Strings(want)
	if err != nil || !reflect.DeepEqual(got.Blocks, want) {
		t.Fatalf("independent roots lost: %+v %v", got, err)
	}
	if _, err := ManifestBlocks(ctx, root, lineage, first); err != nil {
		t.Fatal(err)
	}
	// Damaged metadata never makes a complete supplied inventory disappear.
	// Full block scanning remains authoritative until metadata is repaired.
	path, _ := manifestPath(lineage, first)
	if err := root.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := (BlockScanResult{}).PublishManifestSegments(ctx, root, lineage, all); err != nil {
		t.Fatal(err)
	}
	if _, err := ManifestBlocks(ctx, root, lineage, first); err != nil {
		t.Fatalf("same inventory did not restore segment: %v", err)
	}
}

func TestManifestSegmentsRejectInvalidInputAndCancellation(t *testing.T) {
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, hashes := range [][]string{{"invalid"}, {Hash("same"), Hash("same")}} {
		if _, err := (BlockScanResult{}).PublishManifestSegments(context.Background(), root, lineage, hashes); err == nil {
			t.Fatal("invalid inventory accepted")
		}
	}
	if _, err := (BlockScanResult{Pending: 1}).PublishManifestSegments(context.Background(), root, lineage, nil); err == nil {
		t.Fatal("pending scan accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (BlockScanResult{}).PublishManifestSegments(ctx, root, lineage, nil); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
	if entries, err := os.ReadDir(root.Name()); err != nil || len(entries) != 0 {
		t.Fatalf("invalid call published files: %v %v", entries, err)
	}
	if roots, err := (BlockScanResult{}).PublishManifestSegments(context.Background(), root, lineage, nil); err != nil || len(roots) != 1 {
		t.Fatalf("empty catalog: %v %v", roots, err)
	}
}
