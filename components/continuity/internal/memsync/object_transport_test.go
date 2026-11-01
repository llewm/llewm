package memsync

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitOutputBuffersBoundCopy(t *testing.T) {
	for _, output := range []io.Writer{&boundedTreeBuffer{}, &boundedObjectBuffer{limit: maxCachedTreeBytes}} {
		// Hide the source's WriterTo, as the subprocess pipe reader can do.
		reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", maxCachedTreeBytes+1))}
		if _, err := io.Copy(output, reader); err == nil {
			t.Fatal("io.Copy bypassed subprocess output bound")
		}
	}
}

func objectTransportFixture(t testing.TB, format string) *objectTransport {
	t.Helper()
	directory := t.TempDir()
	repo := git{Dir: directory}
	if _, err := repo.run(context.Background(), "init", "--bare", "--object-format="+format, "."); err != nil {
		if format == "sha256" {
			t.Skipf("Git SHA256 object format unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return &objectTransport{repo: repo}
}

func objectBlob(t testing.TB, transport *objectTransport, name, body string, width int) blockTreeEntry {
	t.Helper()
	oid, err := transport.writeObject(context.Background(), "blob", []byte(body), width)
	if err != nil {
		t.Fatal(err)
	}
	return blockTreeEntry{name: name, kind: "blob", mode: "100644", oid: oid}
}

func objectTree(t testing.TB, transport *objectTransport, entries []blockTreeEntry, width int) string {
	t.Helper()
	oid, err := transport.writeTree(context.Background(), entries, width)
	if err != nil {
		t.Fatal(err)
	}
	return oid
}

func TestObjectTransportBareExactBytesAndTreeOrder(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, format)
			width := 40
			if format == "sha256" {
				width = 64
			}
			body := " \x00binary\nrecord\n\n "
			leaf := objectBlob(t, transport, "space and\nnewline", body, width)
			got, err := transport.readBlob(ctx, leaf, len(body))
			if err != nil || !bytes.Equal(got, []byte(body)) {
				t.Fatalf("object bytes normalized: %q %v", got, err)
			}
			if _, err := transport.readBlob(ctx, leaf, len(body)-1); err == nil {
				t.Fatal("oversize blob accepted")
			}
			child := objectTree(t, transport, []blockTreeEntry{leaf}, width)
			entries := []blockTreeEntry{
				{name: "a", mode: "40000", kind: "tree", oid: child},
				objectBlob(t, transport, "a.c", "opaque", width),
				objectBlob(t, transport, "a0", "other", width),
			}
			root := objectTree(t, transport, entries, width)
			// Git's own fsck verifies ordering, modes and object references.
			if _, err := transport.repo.run(ctx, "fsck", "--strict", "--no-reflogs", root); err != nil {
				t.Fatal(err)
			}
			read, err := transport.trees.read(ctx, transport.repo, root)
			if err != nil || len(read) != 3 || read[0].name != "a.c" || read[1].name != "a" || read[1].mode != "40000" {
				t.Fatalf("wrong canonical tree: %+v %v", read, err)
			}
			for _, mode := range []string{"120000", "100755", "160000"} {
				bad := leaf
				bad.mode = mode
				if _, err := transport.readBlob(ctx, bad, len(body)); err == nil {
					t.Fatalf("unsafe transport mode accepted: %s", mode)
				}
			}
			if _, err := transport.writeTree(ctx, []blockTreeEntry{leaf, leaf}, width); err == nil {
				t.Fatal("duplicate tree name accepted")
			}
			// Bare fixtures never gain a worktree/index as a side effect.
			for _, name := range []string{"index", "a", leaf.name} {
				if _, err := os.Lstat(filepath.Join(transport.repo.Dir, name)); !os.IsNotExist(err) {
					t.Fatalf("worktree/index unexpectedly created: %s %v", name, err)
				}
			}
		})
	}
}

func TestObjectTransportMergeChangedBranchesAndConflicts(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	baseLeaf := objectBlob(t, transport, "immutable", "base", 40)
	baseBranch := objectTree(t, transport, []blockTreeEntry{baseLeaf}, 40)
	// The opaque equal subtree is deliberately unavailable. An unchanged tree
	// identity is reused, so the merge must not list any of its descendants.
	opaque := blockTreeEntry{name: "opaque", kind: "tree", mode: "40000", oid: strings.Repeat("1", 40)}
	branch := func(oid string) blockTreeEntry {
		return blockTreeEntry{name: "selected", kind: "tree", mode: "40000", oid: oid}
	}
	base := objectTree(t, transport, []blockTreeEntry{opaque, branch(baseBranch)}, 40)
	l := objectBlob(t, transport, "local", "local bytes", 40)
	r := objectBlob(t, transport, "remote", "remote bytes", 40)
	localBranch := objectTree(t, transport, []blockTreeEntry{baseLeaf, l}, 40)
	remoteBranch := objectTree(t, transport, []blockTreeEntry{baseLeaf, r}, 40)
	local := objectTree(t, transport, []blockTreeEntry{opaque, branch(localBranch)}, 40)
	remote := objectTree(t, transport, []blockTreeEntry{opaque, branch(remoteBranch)}, 40)
	merged, err := transport.mergeTrees(ctx, base, local, remote)
	if err != nil {
		t.Fatal(err)
	}
	expectedBranch := objectTree(t, transport, []blockTreeEntry{baseLeaf, l, r}, 40)
	expected := objectTree(t, transport, []blockTreeEntry{opaque, branch(expectedBranch)}, 40)
	if merged != expected {
		t.Fatalf("merge differs: %s != %s", merged, expected)
	}
	if len(transport.trees.nodes) != 6 {
		t.Fatalf("merge read unchanged descendants or missed changed nodes: %d", len(transport.trees.nodes))
	}
	before := len(transport.trees.nodes)
	if got, err := transport.mergeTrees(ctx, base, base, remote); err != nil || got != remote || len(transport.trees.nodes) != before {
		t.Fatalf("fast-forward did tree work: %s %v", got, err)
	}
	changed := objectBlob(t, transport, baseLeaf.name, "concurrent change", 40)
	other := objectBlob(t, transport, baseLeaf.name, "different change", 40)
	changeTree := objectTree(t, transport, []blockTreeEntry{changed}, 40)
	otherTree := objectTree(t, transport, []blockTreeEntry{other}, 40)
	if _, err := transport.mergeTrees(ctx, baseBranch, changeTree, otherTree); err == nil {
		t.Fatal("concurrent unequal leaf silently merged")
	}
	empty := objectTree(t, transport, nil, 40)
	if _, err := transport.mergeTrees(ctx, baseBranch, empty, changeTree); err == nil {
		t.Fatal("delete/modify conflict silently merged")
	}
	// Uncontested deletions follow ordinary three-way semantics; transport
	// namespace append-only policy is independently checked before publish.
	if got, err := transport.mergeTrees(ctx, baseBranch, empty, baseBranch); err != nil || got != empty {
		t.Fatalf("uncontested deletion: %s %v", got, err)
	}
}

func TestObjectTransportFixedDeltaSkipsRetainedHistory(t *testing.T) {
	for _, size := range []int{500, 5669, 20000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, "sha1")
			leaf := objectBlob(t, transport, "new", "new object\n", 40)
			retained := make([]blockTreeEntry, size)
			for i := range retained {
				retained[i] = leaf
				retained[i].name = fmt.Sprintf("%08d", i)
			}
			oldBranch := objectTree(t, transport, retained, 40)
			dir := func(name, oid string) blockTreeEntry {
				return blockTreeEntry{name: name, kind: "tree", mode: "40000", oid: oid}
			}
			oldNamespace := objectTree(t, transport, []blockTreeEntry{dir("retained", oldBranch)}, 40)
			newBranch := objectTree(t, transport, []blockTreeEntry{leaf}, 40)
			newNamespace := objectTree(t, transport, []blockTreeEntry{dir("retained", oldBranch), dir("arrivals", newBranch)}, 40)
			before := objectTree(t, transport, []blockTreeEntry{dir("selected", oldNamespace)}, 40)
			after := objectTree(t, transport, []blockTreeEntry{dir("selected", newNamespace)}, 40)
			changes, err := transport.changedLeaves(ctx, before, after, "selected", 1)
			if err != nil || len(changes) != 1 || changes[0].path != "selected/arrivals/new" || changes[0].before.name != "" || changes[0].after != leaf {
				t.Fatalf("fixed delta differs: %+v %v", changes, err)
			}
			if transport.trees.gitReads != 5 || len(transport.trees.nodes) != 5 {
				t.Fatalf("retained history was read: trees=%d nodes=%d", transport.trees.gitReads, len(transport.trees.nodes))
			}
			if _, read := transport.trees.nodes[oldBranch]; read {
				t.Fatal("unchanged retained subtree was opened")
			}
			if same, err := transport.changedLeaves(ctx, after, after, "selected", 1); err != nil || len(same) != 0 || transport.trees.gitReads != 5 {
				t.Fatalf("unchanged tree required work: %v %v", same, err)
			}
			if _, err := transport.changedLeaves(ctx, "", after, "selected", 1); err == nil {
				t.Fatal("initial inventory exceeded limit without fallback")
			}
		})
	}
}

func TestObjectTransportRejectsReplacedBlobAndUnsafeDelta(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	a := objectBlob(t, transport, "record", "original", 40)
	b := objectBlob(t, transport, "record", "replaced", 40)
	if _, err := transport.repo.run(ctx, "replace", a.oid, b.oid); err != nil {
		t.Fatal(err)
	}
	if got, err := transport.readBlob(ctx, a, 100); err != nil || string(got) != "original" {
		t.Fatalf("Git replacement changed transport object: %q %v", got, err)
	}
	if _, err := transport.repo.run(ctx, "replace", "-d", a.oid); err != nil {
		t.Fatal(err)
	}
	dir := func(tree string) blockTreeEntry {
		return blockTreeEntry{name: "selected", kind: "tree", mode: "40000", oid: tree}
	}
	before := objectTree(t, transport, []blockTreeEntry{dir(objectTree(t, transport, []blockTreeEntry{a}, 40))}, 40)
	unsafe := b
	unsafe.mode = "120000"
	after := objectTree(t, transport, []blockTreeEntry{dir(objectTree(t, transport, []blockTreeEntry{unsafe}, 40))}, 40)
	if _, err := transport.changedLeaves(ctx, before, after, "selected", 10); err == nil {
		t.Fatal("selected symlink entered transport delta")
	}
	for _, directory := range []string{"", ".", "..", "../selected", "/selected", "selected/../other", "selected\\other"} {
		if _, err := transport.changedLeaves(ctx, before, before, directory, 10); err == nil {
			t.Fatalf("invalid namespace accepted on unchanged tree: %q", directory)
		}
	}
	empty := objectTree(t, transport, nil, 40)
	deleted, err := transport.changedLeaves(ctx, before, empty, "selected", 1)
	if err != nil || len(deleted) != 1 || deleted[0].before != a || deleted[0].after.name != "" {
		t.Fatalf("namespace deletion lost: %+v %v", deleted, err)
	}
}

func TestObjectTransportRejectsCorruptLooseBlob(t *testing.T) {
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	object := objectBlob(t, transport, "record", "original", 40)
	path := filepath.Join(transport.repo.Dir, "objects", object.oid[:2], object.oid[2:])
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte("blob 8\x00replaced")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.readBlob(ctx, object, 100); err == nil {
		t.Fatal("damaged Git object escaped independent identity check")
	}
}
