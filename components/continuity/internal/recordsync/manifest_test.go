package recordsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func manifestHashes(count int) []string {
	hashes := make([]string, count)
	for i := range hashes {
		hashes[i] = Hash(fmt.Sprintf("block-%d", i))
	}
	return hashes
}

func TestManifestStableSubtreesAndLateArrivals(t *testing.T) {
	hashes := manifestHashes(4096)
	nodes, err := BuildManifest(lineage, hashes)
	if err != nil {
		t.Fatal(err)
	}
	reverse := append([]string(nil), hashes...)
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	repeated, err := BuildManifest(lineage, reverse)
	if err != nil || !reflect.DeepEqual(nodes, repeated) {
		t.Fatalf("input order changed manifest: %v", err)
	}
	added := Hash("late-arrival-with-an-old-recorded-at")
	after, err := BuildManifest(lineage, append(hashes, added))
	if err != nil {
		t.Fatal(err)
	}
	old := make(map[string]Manifest)
	for _, node := range nodes {
		old[node.Prefix] = node
	}
	shared, changed := 0, 0
	for _, node := range after {
		decoded, err := DecodeManifest(lineage, node.Hash, node.Prefix, node.Bytes)
		if err != nil || !bytes.Equal(decoded.Bytes, node.Bytes) {
			t.Fatalf("built invalid manifest: %v", err)
		}
		if prior, ok := old[node.Prefix]; ok {
			if strings.HasPrefix(added, node.Prefix) {
				if prior.Hash == node.Hash {
					t.Fatal("arrival did not change its ancestor")
				}
				changed++
			} else {
				if prior.Hash != node.Hash {
					t.Fatal("unaffected subtree changed")
				}
				shared++
			}
		}
	}
	if shared < 16 || changed < 2 || changed > 65 {
		t.Fatalf("unexpected sharing: %d shared, %d changed", shared, changed)
	}
}

func TestManifestPublicationRecoveryAndConcurrentRoots(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hashes := manifestHashes(1024)
	nodes, err := BuildManifest(lineage, hashes)
	if err != nil {
		t.Fatal(err)
	}
	// Interrupted child publication must not announce a complete snapshot.
	for _, node := range nodes[:len(nodes)/2] {
		path, _ := manifestPath(lineage, node.Hash)
		if _, err := WriteFile(root, path, node.Bytes, false); err != nil {
			t.Fatal(err)
		}
	}
	wantRoot := nodes[len(nodes)-1].Hash
	if _, err := ManifestBlocks(ctx, root, lineage, wantRoot); err == nil {
		t.Fatal("accepted unannounced interrupted publication")
	}
	hash, paths, err := PublishManifest(ctx, root, lineage, hashes)
	if err != nil || hash != wantRoot || len(paths) != len(nodes)+1 {
		t.Fatalf("recover publication: %s %v", hash, err)
	}
	if paths[len(paths)-1] != mustManifestRootPath(t, hash) {
		t.Fatal("root was not published last")
	}
	got, err := ManifestBlocks(ctx, root, lineage, hash)
	sort.Strings(hashes)
	if err != nil || !reflect.DeepEqual(got, hashes) {
		t.Fatalf("manifest lost block identities: %v", err)
	}
	// Independent snapshots announce distinct roots. Neither overwrites the
	// other, even if the second publisher did not observe the first arrival.
	other, _, err := PublishManifest(ctx, root, lineage, []string{Hash("other-writer")})
	if err != nil || other == hash {
		t.Fatalf("concurrent snapshot: %v", err)
	}
	if _, err := ManifestBlocks(ctx, root, lineage, hash); err != nil {
		t.Fatal("another writer retired the first snapshot", err)
	}
	if _, _, err := PublishManifest(ctx, root, lineage, hashes); err != nil {
		t.Fatal("idempotent publication", err)
	}
	// Damage to a child or a root must never be accepted as a complete read.
	childPath, _ := manifestPath(lineage, nodes[0].Hash)
	if err := os.WriteFile(filepath.Join(directory, childPath), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ManifestBlocks(ctx, root, lineage, hash); err == nil {
		t.Fatal("accepted damaged child")
	}
	if _, _, err := PublishManifest(ctx, root, lineage, hashes); err == nil {
		t.Fatal("overwrote immutable damaged child")
	}
}

func mustManifestRootPath(t *testing.T, hash string) string {
	t.Helper()
	path, err := ManifestRootPath(lineage, hash)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManifestRejectsInvalidReferencesAndContexts(t *testing.T) {
	for _, hashes := range [][]string{{"bad"}, {Hash("same"), Hash("same")}} {
		if _, err := BuildManifest(lineage, hashes); err == nil {
			t.Fatal("accepted invalid block identities")
		}
	}
	leaf, err := BuildManifest(lineage, []string{Hash("a")})
	if err != nil {
		t.Fatal(err)
	}
	body := leaf[0].Bytes
	for _, tc := range []struct {
		name, lineage, prefix, hash string
		body                        []byte
	}{
		{"wrong lineage", "lineage:00000000000000000000000000000000", "", leaf[0].Hash, body},
		{"wrong prefix", lineage, "0", leaf[0].Hash, body},
		{"wrong hash", lineage, "", Hash("wrong"), body},
		{"truncated", lineage, "", Hash(string(body[:len(body)-1])), body[:len(body)-1]},
		{"extra newline", lineage, "", Hash(string(body) + "\n"), append(append([]byte(nil), body...), '\n')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeManifest(tc.lineage, tc.hash, tc.prefix, tc.body); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	// A child's declared prefix must be exactly one nibble deeper; arbitrary
	// references cannot create cycles or move records between hash partitions.
	for _, suffix := range []string{
		"children\n00 " + Hash("child") + "\n",
		"children\n0 " + Hash("child") + "\n0 " + Hash("other") + "\n",
		"children\n",
		"blocks\n" + Hash("a") + "\n" + Hash("a") + "\n",
	} {
		body := []byte(manifestHeader + lineage + "\n\n" + suffix)
		if _, err := DecodeManifest(lineage, Hash(string(body)), "", body); err == nil {
			t.Fatalf("accepted malformed references: %q", suffix)
		}
	}
}

func TestManifestEmptyAndCancelledPublication(t *testing.T) {
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hash, _, err := PublishManifest(context.Background(), root, lineage, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ManifestBlocks(context.Background(), root, lineage, hash)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty manifest: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PublishManifest(ctx, root, lineage, manifestHashes(2)); err != context.Canceled {
		t.Fatalf("cancelled publication: %v", err)
	}
}

func TestManifestDeltaMatchesCompleteInventories(t *testing.T) {
	ctx := context.Background()
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, count := range []int{0, 128, 129, 4096} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			before := manifestHashes(count)
			old, _, err := PublishManifest(ctx, root, lineage, before)
			if err != nil {
				t.Fatal(err)
			}
			// Read the old inventory completely before asking for a delta.
			if _, err := ManifestBlocks(ctx, root, lineage, old); err != nil {
				t.Fatal(err)
			}
			arrival := Hash(fmt.Sprintf("late-%d", count))
			after := append(append([]string(nil), before...), arrival)
			current, _, err := PublishManifest(ctx, root, lineage, after)
			if err != nil {
				t.Fatal(err)
			}
			delta, err := ChangedManifestBlocks(ctx, root, lineage, old, current)
			if err != nil || !reflect.DeepEqual(delta.Added, []string{arrival}) || len(delta.Removed) != 0 {
				t.Fatalf("addition differs from full inventory: %+v %v", delta, err)
			}
			if count == 4096 && delta.VisitedNodes > 6 {
				t.Fatalf("small arrival visited old subtrees: %+v", delta)
			}
			reversed, err := ChangedManifestBlocks(ctx, root, lineage, current, old)
			if err != nil || len(reversed.Added) != 0 || !reflect.DeepEqual(reversed.Removed, []string{arrival}) {
				t.Fatalf("removal differs from full inventory: %+v %v", reversed, err)
			}
			unchanged, err := ChangedManifestBlocks(ctx, root, lineage, old, old)
			if err != nil || unchanged.VisitedNodes != 0 || len(unchanged.Added)+len(unchanged.Removed) != 0 {
				t.Fatalf("unchanged catalog not reused: %+v %v", unchanged, err)
			}
		})
	}
}

func TestManifestDeltaRejectsMissingAnnouncementAndChangedNodes(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, _, err := PublishManifest(ctx, root, lineage, manifestHashes(256))
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := PublishManifest(ctx, root, lineage, manifestHashes(257))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ChangedManifestBlocks(ctx, root, lineage, before, Hash("missing")); err == nil {
		t.Fatal("treated missing root as empty")
	}
	path, _ := manifestPath(lineage, after)
	if err := os.WriteFile(filepath.Join(directory, path), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if delta, err := ChangedManifestBlocks(ctx, root, lineage, before, after); err == nil || len(delta.Added)+len(delta.Removed) != 0 {
		t.Fatal("accepted damaged changed node or returned partial delta")
	}
}
