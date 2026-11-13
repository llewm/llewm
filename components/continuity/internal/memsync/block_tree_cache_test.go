package memsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestBlockTreeCacheAcrossCyclesAndCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	repo := transport.repo
	directory, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := openBlockTreeCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	directory = filepath.ToSlash(directory)
	files := map[string]blockTreeEntry{}
	root := fixturePathTree(t, transport, files, 40)
	put := func(name string) {
		t.Helper()
		files[directory+"/"+name] = objectBlob(t, transport, filepath.Base(name), name, 40)
		root = fixturePathTree(t, transport, files, 40)
	}
	check := func() *blockTreeDiscovery {
		t.Helper()
		reader := &objectTransport{repo: repo, trees: blockTreeDiscovery{cache: cache}}
		changes, err := reader.changedLeaves(ctx, "", root, directory, recordsync.MaxScanEntries)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, change := range changes {
			got = append(got, change.path)
		}
		listing, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "ls-tree", "-r", "--name-only", "-z", root, "--", directory)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		if len(listing) != 0 {
			want = strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00")
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cache=%q Git=%q", got, want)
		}
		return &reader.trees
	}
	put("a/first.block")
	put("b/space and\nnewline.block")
	first := check()
	if first.gitReads == 0 {
		t.Fatal("cold cache did not read Git")
	}
	next := check()
	if next.gitReads != 0 || next.cacheHits == 0 {
		t.Fatalf("new cycle reread Git: reads=%d hits=%d", next.gitReads, next.cacheHits)
	}
	put("a/late.block")
	changed := check()
	if changed.gitReads == 0 || changed.cacheHits == 0 {
		t.Fatalf("changed branch failed to reuse old trees: reads=%d hits=%d", changed.gitReads, changed.cacheHits)
	}
	var branchB string
	for oid, entries := range changed.nodes {
		if len(entries) == 1 && entries[0].name == "space and\nnewline.block" {
			branchB = oid
		}
	}
	if branchB == "" {
		t.Fatal("missing unchanged branch")
	}
	if err := cache.WriteFile(branchB+".tree", []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	repaired := check()
	if repaired.gitReads != 1 {
		t.Fatalf("repair read %d Git nodes, want one", repaired.gitReads)
	}
	if again := check(); again.gitReads != 0 {
		t.Fatal("repair was not persisted")
	}
	delete(files, directory+"/a/first.block")
	root = fixturePathTree(t, transport, files, 40)
	check() // A removed path must not be resurrected from an older tree.
	cache.Close()
	if unavailable := check(); unavailable.gitReads == 0 {
		t.Fatal("unavailable cache did not fall back to Git")
	}
}

func TestGitTreeCacheBoundsAndIdentity(t *testing.T) {
	t.Parallel()
	raw := append([]byte("100644 file\x00"), make([]byte, 32)...)
	framed := append([]byte(fmt.Sprintf("tree %d\x00", len(raw))), raw...)
	sum := sha256.Sum256(framed)
	entries, err := decodeGitTree(hex.EncodeToString(sum[:]), raw)
	if err != nil || len(entries) != 1 || entries[0].name != "file" || entries[0].kind != "blob" || entries[0].oid != strings.Repeat("0", 64) {
		t.Fatalf("SHA-256 tree: %+v %v", entries, err)
	}
	var output boundedTreeBuffer
	if _, err := output.Write(make([]byte, maxCachedTreeBytes+1)); err == nil || output.Len() != 0 {
		t.Fatal("unbounded Git output accepted")
	}
	for _, oid := range []string{strings.Repeat("0", 40), strings.Repeat("0", 64), "invalid"} {
		if _, err := decodeGitTree(oid, []byte("unverified membership")); err == nil {
			t.Fatal("unverified tree accepted")
		}
	}
}
