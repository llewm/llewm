package memsync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Structural fixtures deliberately share one blob: radix routines validate
// paths/identities, while their transport caller must decode actual block bytes.
func radixFixtureLeaves(t testing.TB, transport *objectTransport, size, width int, deep bool) map[string]blockTreeEntry {
	t.Helper()
	blob := objectBlob(t, transport, "unused", "structural fixture", width)
	leaves := map[string]blockTreeEntry{}
	for i := 0; i < size; i++ {
		hash := fmt.Sprintf("%064x", i)
		if !deep {
			hash = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(i))))
		}
		leaf := blob
		leaf.name = hash + ".block"
		leaves[leaf.name] = leaf
	}
	return leaves
}

func TestObjectRadixSplitPreservesExactBlocks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, format)
			width := 40
			if format == "sha256" {
				width = 64
			}
			leaves := map[string]blockTreeEntry{}
			bodies := map[string]recordsync.Block{}
			for i := 0; i < 65; i++ {
				block := transportBlockFixture(t, testLineage, fmt.Sprintf("split-%d", i))
				leaf := objectBlob(t, transport, block.Hash+".block", string(block.Bytes), width)
				leaves[leaf.name], bodies[leaf.name] = leaf, block
			}
			names := radixNames(leaves)
			last := leaves[names[64]]
			delete(leaves, last.name)
			before, added, err := transport.appendRadix(ctx, "", leaves, width)
			if err != nil || len(added) != 64 {
				t.Fatalf("initial: %d %v", len(added), err)
			}
			after, added, err := transport.appendRadix(ctx, before, map[string]blockTreeEntry{last.name: last}, width)
			if err != nil || len(added) != 1 {
				t.Fatalf("split: %d %v", len(added), err)
			}
			delta, err := transport.radixDelta(ctx, before, after, 1)
			if err != nil || len(delta) != 1 || delta[0] != last {
				t.Fatalf("split delta: %+v %v", delta, err)
			}
			all, err := transport.radixDelta(ctx, "", after, 65)
			if err != nil || len(all) != 65 {
				t.Fatalf("full: %d %v", len(all), err)
			}
			for _, leaf := range all {
				body, err := transport.readBlob(ctx, leaf, recordsync.MaxBlockBytes)
				if err != nil || string(body) != string(bodies[leaf.name].Bytes) {
					t.Fatalf("changed block bytes: %v", err)
				}
			}
			again, added, err := transport.appendRadix(ctx, after, leaves, width)
			if err != nil || again != after || len(added) != 0 {
				t.Fatalf("not idempotent: %d %v", len(added), err)
			}
			if _, err := transport.repo.run(ctx, "fsck", "--strict", "--no-reflogs", after); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObjectRadixBoundedDiscoveryAndDeepPrefixes(t *testing.T) {
	for _, size := range []int{500, 5669, 10000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, "sha1")
			leaves := radixFixtureLeaves(t, transport, size, 40, false)
			names := radixNames(leaves)
			arrival := leaves[names[len(names)-1]]
			delete(leaves, arrival.name)
			before, _, err := transport.appendRadix(ctx, "", leaves, 40)
			if err != nil {
				t.Fatal(err)
			}
			after, _, err := transport.appendRadix(ctx, before, map[string]blockTreeEntry{arrival.name: arrival}, 40)
			if err != nil {
				t.Fatal(err)
			}
			reader := &objectTransport{repo: transport.repo}
			measured, metrics := syncwork.WithWorkMetrics(ctx)
			delta, err := reader.radixDelta(measured, before, after, 1)
			if err != nil || len(delta) != 1 || delta[0] != arrival {
				t.Fatalf("delta: %+v %v", delta, err)
			}
			counts := metrics.Counts()
			if counts["transport_tree_entries_read"] > 640 || counts["transport_blobs_read"] != 0 {
				t.Fatalf("unbounded discovery: %v", counts)
			}
			t.Logf("%d records: %v", size, counts)
		})
	}
	t.Run("31 shared hash bytes", func(t *testing.T) {
		transport := objectTransportFixture(t, "sha1")
		leaves := radixFixtureLeaves(t, transport, 65, 40, true)
		tree, _, err := transport.appendRadix(context.Background(), "", leaves, 40)
		if err != nil {
			t.Fatal(err)
		}
		all, err := transport.radixDelta(context.Background(), "", tree, 65)
		if err != nil || len(all) != 65 {
			t.Fatalf("deep: %d %v", len(all), err)
		}
	})
}

func TestObjectRadixRejectsNoncanonicalAndDestructiveChanges(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	leaves := radixFixtureLeaves(t, transport, 65, 40, false)
	before, _, err := transport.appendRadix(ctx, "", leaves, 40)
	if err != nil {
		t.Fatal(err)
	}
	leaf := leaves[radixNames(leaves)[0]]
	for _, mode := range []string{"deletion", "replacement", "wrong-prefix", "mixed", "small-branch", "empty-branch", "oversize-leaf", "mode"} {
		t.Run(mode, func(t *testing.T) {
			base := ""
			var after string
			switch mode {
			case "deletion", "replacement":
				base = before
				copy := map[string]blockTreeEntry{}
				for name, entry := range leaves {
					copy[name] = entry
				}
				delete(copy, leaf.name)
				if mode == "replacement" {
					copy[leaf.name] = objectBlob(t, transport, leaf.name, "replacement", 40)
				}
				after, _, err = transport.appendRadix(ctx, "", copy, 40)
			case "wrong-prefix", "small-branch", "empty-branch":
				part := leaf.name[:2]
				child := objectTree(t, transport, []blockTreeEntry{leaf}, 40)
				if mode == "wrong-prefix" {
					if part == "ff" {
						part = "00"
					} else {
						part = "ff"
					}
				}
				if mode == "empty-branch" {
					child = objectTree(t, transport, nil, 40)
				}
				after = objectTree(t, transport, []blockTreeEntry{{name: part, kind: "tree", mode: "40000", oid: child}}, 40)
			case "mixed":
				child := objectTree(t, transport, []blockTreeEntry{leaf}, 40)
				after = objectTree(t, transport, []blockTreeEntry{leaf, {name: "ff", kind: "tree", mode: "40000", oid: child}}, 40)
			case "oversize-leaf":
				var entries []blockTreeEntry
				for _, entry := range leaves {
					entries = append(entries, entry)
				}
				after = objectTree(t, transport, entries, 40)
			case "mode":
				bad := leaf
				bad.mode = "120000"
				after = objectTree(t, transport, []blockTreeEntry{bad}, 40)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.radixDelta(ctx, base, after, 100); err == nil {
				t.Fatal("accepted invalid radix history")
			}
		})
	}
	if _, err := transport.radixDelta(ctx, "", before, 64); err == nil {
		t.Fatal("ignored delta limit")
	}
	bad := leaf
	bad.name = strings.Repeat("z", 64) + ".block"
	if _, _, err := transport.appendRadix(ctx, before, map[string]blockTreeEntry{bad.name: bad}, 40); err == nil {
		t.Fatal("accepted invalid addition")
	}
}
