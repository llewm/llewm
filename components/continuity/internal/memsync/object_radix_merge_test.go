package memsync

import (
	"context"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestObjectRadixNormalizesConcurrentBucketSplits(t *testing.T) {
	for _, tc := range []struct {
		name                string
		base, local, remote int
		deep                bool
	}{
		{"two overflow a leaf", 63, 1, 1, false},
		{"one splits while other appends", 63, 1, 2, false},
		{"both split", 64, 1, 1, false},
		{"both split deep prefix", 64, 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, "sha1")
			all := radixFixtureLeaves(t, transport, tc.base+tc.local+tc.remote, 40, tc.deep)
			names := radixNames(all)
			baseLeaves, localLeaves, remoteLeaves := map[string]blockTreeEntry{}, map[string]blockTreeEntry{}, map[string]blockTreeEntry{}
			for i, name := range names {
				switch {
				case i < tc.base:
					baseLeaves[name] = all[name]
				case i < tc.base+tc.local:
					localLeaves[name] = all[name]
				default:
					remoteLeaves[name] = all[name]
				}
			}
			base, _, err := transport.appendRadix(ctx, "", "", baseLeaves, 40)
			if err != nil {
				t.Fatal(err)
			}
			local, _, err := transport.appendRadix(ctx, base, "", localLeaves, 40)
			if err != nil {
				t.Fatal(err)
			}
			remote, _, err := transport.appendRadix(ctx, base, "", remoteLeaves, 40)
			if err != nil {
				t.Fatal(err)
			}
			mergeBase, mergeLocal, mergeRemote := base, local, remote
			if tc.deep {
				wrap := func(tree string) string {
					selected := objectTree(t, transport, []blockTreeEntry{{name: "blocks", kind: "tree", mode: "40000", oid: tree}}, 40)
					partition := objectTree(t, transport, []blockTreeEntry{{name: "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a", kind: "tree", mode: "40000", oid: selected}}, 40)
					return objectTree(t, transport, []blockTreeEntry{{name: "v5", kind: "tree", mode: "40000", oid: partition}}, 40)
				}
				mergeBase, mergeLocal, mergeRemote = wrap(base), wrap(local), wrap(remote)
			}
			merged, err := transport.mergeTrees(ctx, mergeBase, mergeLocal, mergeRemote)
			if err != nil {
				t.Fatal(err)
			}
			if tc.deep {
				merged, err = transport.treeAt(ctx, merged, "v5", "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a", "blocks")
				if err != nil {
					t.Fatal(err)
				}
			}
			normalized, err := transport.normalizeRadix(ctx, merged, "", 40)
			if err != nil {
				t.Fatal(err)
			}
			for _, prior := range []string{local, remote} {
				if _, err := transport.radixDelta(ctx, prior, normalized, len(all)); err != nil {
					t.Fatalf("lost a parent block: %v", err)
				}
			}
			inventory, err := transport.radixDelta(ctx, "", normalized, len(all))
			if err != nil || len(inventory) != len(all) {
				t.Fatalf("inventory %d/%d: %v", len(inventory), len(all), err)
			}
			canonical, _, err := transport.appendRadix(ctx, "", "", all, 40)
			if err != nil || normalized != canonical {
				t.Fatalf("noncanonical merge %s/%s: %v", normalized, canonical, err)
			}
		})
	}
}

func TestObjectRadixNormalizationSkipsUnchangedInputTrees(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	leaves := radixFixtureLeaves(t, transport, 10000, 40, false)
	names := radixNames(leaves)
	one, two := leaves[names[0]], leaves[names[len(names)-1]]
	delete(leaves, one.name)
	delete(leaves, two.name)
	base, _, err := transport.appendRadix(ctx, "", "", leaves, 40)
	if err != nil {
		t.Fatal(err)
	}
	local, _, err := transport.appendRadix(ctx, base, "", map[string]blockTreeEntry{one.name: one}, 40)
	if err != nil {
		t.Fatal(err)
	}
	remote, _, err := transport.appendRadix(ctx, base, "", map[string]blockTreeEntry{two.name: two}, 40)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := transport.mergeTrees(ctx, base, local, remote)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh tree reader makes any accidental revisit visible in work counts.
	transport.trees = blockTreeDiscovery{}
	measured, metrics := syncwork.WithWorkMetrics(ctx)
	normalized, err := transport.normalizeRadix(measured, merged, "", 40)
	if err != nil {
		t.Fatal(err)
	}
	if counts := metrics.Counts(); counts["transport_tree_entries_read"] > 384 || counts["transport_blobs_read"] != 0 {
		t.Fatalf("normalization rewalked history: %v", counts)
	}
	if delta, err := transport.radixDelta(ctx, local, normalized, 1); err != nil || len(delta) != 1 || delta[0] != two {
		t.Fatalf("arrival: %+v %v", delta, err)
	}
}

func TestObjectRadixMixedDuplicateMustRetainExactIdentity(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, "sha1")
			leaves := radixFixtureLeaves(t, transport, 65, 40, false)
			tree, _, err := transport.appendRadix(ctx, "", "", leaves, 40)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := transport.trees.read(ctx, transport.repo, tree)
			if err != nil {
				t.Fatal(err)
			}
			leaf := leaves[radixNames(leaves)[0]]
			if conflict {
				leaf = objectBlob(t, transport, leaf.name, "conflict", 40)
			}
			mixed := objectTree(t, transport, append(append([]blockTreeEntry(nil), entries...), leaf), 40)
			normalized, err := transport.normalizeRadix(ctx, mixed, "", 40)
			if conflict {
				if err == nil {
					t.Fatal("accepted conflicting relocated block")
				}
				return
			}
			if err != nil || normalized != tree {
				t.Fatalf("duplicate changed tree: %v", err)
			}
		})
	}
}
