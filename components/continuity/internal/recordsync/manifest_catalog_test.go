package recordsync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestManifestCatalogUnionsIndependentSnapshotsAndSharesReads(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	base := manifestHashes(4096)
	left := append(append([]string(nil), base...), Hash("writer-left"))
	right := append(append([]string(nil), base...), Hash("writer-right"))
	// Neither concurrent publisher saw the other's arrival. A smaller snapshot
	// published last must not retire either writer's records.
	snapshots := [][]string{base, left, right, {Hash("independent-writer")}, nil}
	var roots []string
	nodes := make(map[string]bool)
	union := make(map[string]bool)
	for _, snapshot := range snapshots {
		hash, _, err := PublishManifest(ctx, root, lineage, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		roots = append(roots, hash)
		built, err := BuildManifest(lineage, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range built {
			nodes[node.Hash] = true
		}
		for _, block := range snapshot {
			union[block] = true
		}
	}
	var want []string
	for hash := range union {
		want = append(want, hash)
	}
	sort.Strings(want)
	sort.Strings(roots)
	got, err := DiscoverManifestCatalog(ctx, root, lineage)
	if err != nil || !reflect.DeepEqual(got.Roots, roots) || !reflect.DeepEqual(got.Blocks, want) {
		t.Fatalf("lost concurrent snapshot membership: %+v %v", got, err)
	}
	if got.VisitedNodes != len(nodes) {
		t.Fatalf("shared nodes reread: got %d, unique %d", got.VisitedNodes, len(nodes))
	}
	// Catalog membership is not evidence of block availability. These snapshots
	// intentionally have no block files; a transport still needs block checks.
	scan, err := ScanBlocks(ctx, root, lineage)
	if err != nil || len(scan.Blocks) != 0 {
		t.Fatalf("catalog altered complete block inventory: %v", err)
	}
}

func TestManifestCatalogRejectsMissingDamagedAndUnexpectedMetadata(t *testing.T) {
	for _, damage := range []string{"absent", "empty directory", "only staged", "conflict copy", "directory entry", "symlink entry", "bad announcement", "missing child", "bad child", "symlink directory"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			root, err := OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			path, err := ManifestRootPath(lineage, Hash("placeholder"))
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Dir(path)
			if damage != "absent" {
				hash, _, err := PublishManifest(ctx, root, lineage, manifestHashes(256))
				if err != nil {
					t.Fatal(err)
				}
				announcement, _ := ManifestRootPath(lineage, hash)
				nodes, _ := BuildManifest(lineage, manifestHashes(256))
				child, _ := manifestPath(lineage, nodes[0].Hash)
				switch damage {
				case "empty directory", "only staged":
					err = root.Remove(announcement)
					if err == nil && damage == "only staged" {
						_, err = WriteFile(root, filepath.Join(parent, ".llewm-stage-interrupted"), []byte("partial"), false)
					}
				case "conflict copy":
					_, err = WriteFile(root, announcement+" (conflict)", []byte("conflict"), false)
				case "directory entry":
					err = root.Mkdir(path, 0700)
				case "symlink entry":
					err = os.Symlink(filepath.Base(announcement), filepath.Join(directory, path))
				case "bad announcement":
					err = os.WriteFile(filepath.Join(directory, announcement), []byte("damaged"), 0600)
				case "missing child":
					err = root.Remove(child)
				case "bad child":
					err = os.WriteFile(filepath.Join(directory, child), []byte("damaged"), 0600)
				case "symlink directory":
					err = root.Rename(parent, parent+"-real")
					if err == nil {
						err = os.Symlink(filepath.Base(parent)+"-real", filepath.Join(directory, parent))
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := DiscoverManifestCatalog(ctx, root, lineage)
			if err == nil || !reflect.DeepEqual(got, ManifestCatalog{}) {
				t.Fatalf("accepted uncertain metadata or leaked partial inventory: %+v %v", got, err)
			}
		})
	}
}

func TestManifestCatalogEmptySnapshotAndStagedPublication(t *testing.T) {
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hash, _, err := PublishManifest(context.Background(), root, lineage, nil)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := ManifestRootPath(lineage, hash)
	if _, err := WriteFile(root, filepath.Join(filepath.Dir(path), ".llewm-stage-interrupted"), []byte("incomplete"), false); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverManifestCatalog(context.Background(), root, lineage)
	if err != nil || !reflect.DeepEqual(got.Roots, []string{hash}) || len(got.Blocks) != 0 || got.VisitedNodes != 1 {
		t.Fatalf("announced empty snapshot: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := DiscoverManifestCatalog(ctx, root, lineage); err != context.Canceled || !reflect.DeepEqual(got, ManifestCatalog{}) {
		t.Fatalf("cancelled discovery: %+v %v", got, err)
	}
}

// Schedule a root publication after selection and before traversal, without a
// timing-dependent goroutine or production fault-injection hook.
type manifestPublicationContext struct {
	context.Context
	checks int
	add    func()
}

func (c *manifestPublicationContext) Err() error {
	c.checks++
	if c.checks == 3 {
		c.add()
	}
	return c.Context.Err()
}

func TestManifestCatalogRejectsConcurrentRootPublication(t *testing.T) {
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := PublishManifest(context.Background(), root, lineage, manifestHashes(2)); err != nil {
		t.Fatal(err)
	}
	ctx := &manifestPublicationContext{Context: context.Background(), add: func() {
		if _, _, err := PublishManifest(context.Background(), root, lineage, []string{Hash("concurrent-arrival")}); err != nil {
			t.Fatal(err)
		}
	}}
	if got, err := DiscoverManifestCatalog(ctx, root, lineage); err == nil || !reflect.DeepEqual(got, ManifestCatalog{}) {
		t.Fatalf("accepted root publication race: %+v %v", got, err)
	}
	// Retrying from the durable announcements recovers both independent roots.
	got, err := DiscoverManifestCatalog(context.Background(), root, lineage)
	if err != nil || len(got.Roots) != 2 || len(got.Blocks) != 3 {
		t.Fatalf("retry lost a root: %+v %v", got, err)
	}
}

func TestManifestCatalogBoundsUnionAcrossRoots(t *testing.T) {
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hashes := manifestHashes(portable.MaxRecordCount + 1)
	// Each snapshot is within bounds; only their disjoint union exceeds them.
	for _, snapshot := range [][]string{hashes[:len(hashes)/2], hashes[len(hashes)/2:]} {
		if _, _, err := PublishManifest(context.Background(), root, lineage, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := DiscoverManifestCatalog(context.Background(), root, lineage); err == nil || !reflect.DeepEqual(got, ManifestCatalog{}) {
		t.Fatalf("accepted oversized union or leaked partial inventory: %d blocks %v", len(got.Blocks), err)
	}
}
