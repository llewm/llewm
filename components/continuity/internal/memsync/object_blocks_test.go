package memsync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func transportBlockFixture(t testing.TB, lineage, id string) recordsync.Block {
	t.Helper()
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordedAt: time.Unix(1, 0).UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := recordsync.EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func transportBlockTree(t testing.TB, transport *objectTransport, version, lineage string, leaves []blockTreeEntry, width int) string {
	t.Helper()
	blocks := objectTree(t, transport, leaves, width)
	selected := objectTree(t, transport, []blockTreeEntry{{name: "blocks", kind: "tree", mode: "40000", oid: blocks}}, width)
	partition := objectTree(t, transport, []blockTreeEntry{{name: strings.TrimPrefix(lineage, "lineage:"), kind: "tree", mode: "40000", oid: selected}}, width)
	return objectTree(t, transport, []blockTreeEntry{{name: version, kind: "tree", mode: "40000", oid: partition}}, width)
}

func TestObjectBlocksAppendDeltaAndOpaqueLineages(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, format)
			width := 40
			if format == "sha256" {
				width = 64
			}
			other := "lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			root := objectTree(t, transport, []blockTreeEntry{objectBlob(t, transport, "README", "opaque root bytes", width)}, width)
			otherBlock := transportBlockFixture(t, other, "other")
			before, _, err := transport.appendBlocks(ctx, root, other, []recordsync.Block{otherBlock}, width)
			if err != nil {
				t.Fatal(err)
			}
			one := transportBlockFixture(t, testLineage, "one")
			two := transportBlockFixture(t, testLineage, "two")
			first, count, err := transport.appendBlocks(ctx, before, testLineage, []recordsync.Block{one}, width)
			if err != nil || count != 1 {
				t.Fatalf("first publication: %d %v", count, err)
			}
			after, count, err := transport.appendBlocks(ctx, first, testLineage, []recordsync.Block{one, two}, width)
			if err != nil || count != 1 {
				t.Fatalf("second publication: %d %v", count, err)
			}
			delta, err := transport.blockDelta(ctx, first, after, testLineage)
			if err != nil || len(delta) != 1 || delta[0].Hash != two.Hash {
				t.Fatalf("incorrect arrival: %+v %v", delta, err)
			}
			all, err := transport.blockDelta(ctx, "", after, testLineage)
			if err != nil || len(all) != 2 {
				t.Fatalf("initial inventory: %d %v", len(all), err)
			}
			otherBefore, err := transport.treeAt(ctx, before, "v5", strings.TrimPrefix(other, "lineage:"))
			if err != nil {
				t.Fatal(err)
			}
			otherAfter, err := transport.treeAt(ctx, after, "v5", strings.TrimPrefix(other, "lineage:"))
			if err != nil || otherBefore != otherAfter {
				t.Fatalf("unrelated lineage rewritten: %v", err)
			}
			again, count, err := transport.appendBlocks(ctx, after, testLineage, []recordsync.Block{two, one}, width)
			if err != nil || count != 0 || again != after {
				t.Fatalf("publication not idempotent: %s %d %v", again, count, err)
			}
			if _, err := transport.repo.run(ctx, "fsck", "--strict", "--no-reflogs", after); err != nil {
				t.Fatal(err)
			}
			if matches, _ := filepath.Glob(filepath.Join(transport.repo.Dir, "v5", "*")); len(matches) != 0 {
				t.Fatal("transport wrote a working tree")
			}
		})
	}
}

func TestObjectBlocksRejectMutationModesPathsAndUnknownSelectedFormats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	block := transportBlockFixture(t, testLineage, "retained")
	leaf := objectBlob(t, transport, block.Hash+".block", string(block.Bytes), 40)
	before := transportBlockTree(t, transport, "v5", testLineage, []blockTreeEntry{leaf}, 40)
	for _, mode := range []string{"deleted", "replaced", "symlink", "executable", "wrong-name", "bad-bytes", "future", "former"} {
		t.Run(mode, func(t *testing.T) {
			candidate := leaf
			version := "v5"
			switch mode {
			case "deleted":
				// Empty selected inventory must not authorize record removal.
			case "replaced", "bad-bytes":
				candidate = objectBlob(t, transport, leaf.name, "invalid block bytes", 40)
			case "symlink":
				candidate.mode = "120000"
			case "executable":
				candidate.mode = "100755"
			case "wrong-name":
				candidate.name = "noncanonical.block"
			case "future":
				version = "v6"
			case "former":
				version = "v3"
			}
			var leaves []blockTreeEntry
			if mode != "deleted" {
				leaves = []blockTreeEntry{candidate}
			}
			after := transportBlockTree(t, transport, version, testLineage, leaves, 40)
			base := before
			if mode == "bad-bytes" || mode == "symlink" || mode == "executable" || mode == "wrong-name" {
				base = "" // Initial/new arrivals must verify bytes, paths and modes too.
			}
			if _, err := transport.blockDelta(ctx, base, after, testLineage); err == nil {
				t.Fatal("invalid block change accepted")
			}
		})
	}
	// Unknown formats in another lineage are opaque and retain their identity.
	other := "lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	opaque := transportBlockTree(t, transport, "v99", other, []blockTreeEntry{leaf}, 40)
	if _, _, err := transport.appendBlocks(ctx, opaque, testLineage, []recordsync.Block{block}, 40); err != nil {
		t.Fatalf("unrelated future lineage blocked publication: %v", err)
	}
}

func TestObjectBlocksMergeValidatesArrivalsBeforeJoiningHistories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	transport.repo.Identity = []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
	baseBlock := transportBlockFixture(t, testLineage, "base")
	localBlock := transportBlockFixture(t, testLineage, "local")
	remoteBlock := transportBlockFixture(t, testLineage, "remote")
	baseTree, _, err := transport.appendBlocks(ctx, "", testLineage, []recordsync.Block{baseBlock}, 40)
	if err != nil {
		t.Fatal(err)
	}
	base, err := transport.writeCommit(ctx, baseTree, nil, "base")
	if err != nil {
		t.Fatal(err)
	}
	localTree, _, err := transport.appendBlocks(ctx, baseTree, testLineage, []recordsync.Block{localBlock}, 40)
	if err != nil {
		t.Fatal(err)
	}
	remoteTree, _, err := transport.appendBlocks(ctx, baseTree, testLineage, []recordsync.Block{remoteBlock}, 40)
	if err != nil {
		t.Fatal(err)
	}
	local, err := transport.writeCommit(ctx, localTree, []string{base}, "local")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := transport.writeCommit(ctx, remoteTree, []string{base}, "remote")
	if err != nil {
		t.Fatal(err)
	}
	validate := func(ctx context.Context, b, l, r string) error {
		if _, err := transport.blockDelta(ctx, b, l, testLineage); err != nil {
			return err
		}
		_, err := transport.blockDelta(ctx, b, r, testLineage)
		return err
	}
	merged, err := transport.mergeCommits(ctx, local, remote, "join verified history", validate, transport.finishBlockMerge(testLineage, false))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := transport.readCommit(ctx, merged)
	if err != nil {
		t.Fatal(err)
	}
	all, err := transport.blockDelta(ctx, "", commit.Tree, testLineage)
	if err != nil || len(all) != 3 {
		t.Fatalf("merge lost immutable observations: %d %v", len(all), err)
	}
	got := map[string]bool{}
	for _, block := range all {
		got[block.Hash] = true
	}
	for _, block := range []recordsync.Block{baseBlock, localBlock, remoteBlock} {
		if !got[block.Hash] {
			t.Fatalf("merge replaced block %s", block.Hash)
		}
	}
	removedTree := transportBlockTree(t, transport, "v5", testLineage, []blockTreeEntry{objectBlob(t, transport, remoteBlock.Hash+".block", string(remoteBlock.Bytes), 40)}, 40)
	removed, err := transport.writeCommit(ctx, removedTree, []string{base}, "invalid removal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.mergeCommits(ctx, local, removed, "must refuse", validate, nil); err == nil {
		t.Fatal("merge adopted a removed immutable observation")
	}
}
