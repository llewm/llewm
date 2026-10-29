package memsync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestBlockTreeDiscoveryMatchesIndexAcrossChanges(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "tree-discovery", bareRemote(t))
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	d := &blockTreeDiscovery{}
	m.syncer.discovery = d
	put := func(name string) {
		t.Helper()
		name = filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(m.syncer.Clone, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m.syncer.Clone, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.run(ctx, "add", "--", name); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		got, err := m.syncer.trackedBlockPaths(ctx, repo, directory)
		if err != nil {
			t.Fatal(err)
		}
		listing, err := repo.run(ctx, "ls-files", "-z", "--", directory)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		if listing != "" {
			want = strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00")
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("tree paths=%q index=%q", got, want)
		}
	}
	check() // Empty/unborn HEAD; index is still authoritative.
	put("branch-a/first.block")
	put("branch-b/space and\nnewline.block")
	check() // Staged, uncommitted additions must be visible.
	entries := d.entries
	check()
	if d.entries != entries {
		t.Fatal("unchanged trees were rediscovered")
	}
	if _, err := repo.run(ctx, "commit", "-m", "fixture: initial trees"); err != nil {
		t.Fatal(err)
	}
	check()
	if d.entries != entries {
		t.Fatal("commit changed identical index tree discovery")
	}
	put("branch-a/late-arrival.block")
	check()
	// The changed branch is read, while branch-b's exact node remains cached.
	var branchB string
	for oid, values := range d.nodes {
		if len(values) == 1 && values[0].name == "space and\nnewline.block" {
			branchB = oid
		}
	}
	if branchB == "" {
		t.Fatal("unchanged branch not retained")
	}
	if _, err := repo.run(ctx, "rm", "--", filepath.Join(directory, "branch-a/first.block")); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := repo.run(ctx, "reset", "--hard", "HEAD"); err != nil {
		t.Fatal(err)
	}
	check() // Interrupted publication/reset must recover exact old membership.
	d.entries = recordsync.MaxScanEntries
	put("branch-a/over-cache-limit.block")
	check() // Cache limits must fall back to a complete index listing.
	if _, err := repo.run(ctx, "reset", "--hard", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "rm", "-r", "--", directory); err != nil {
		t.Fatal(err)
	}
	check() // Removed namespace must not reuse an old inventory.
}

func TestBlockTreeDiscoveryUnmergedIndexFallsBack(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "unmerged-discovery", bareRemote(t))
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _ := recordsync.BlockDirectory(testLineage)
	name := directory + "/unmerged.block"
	if err := os.MkdirAll(filepath.Join(m.syncer.Clone, directory), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.syncer.Clone, name), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	oid, err := repo.run(ctx, "hash-object", "-w", "--", name)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.runInput(ctx, "100644 "+oid+" 1\t"+name+"\n100644 "+oid+" 2\t"+name+"\n", "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "write-tree"); err == nil {
		t.Fatal("fixture is not unmerged")
	}
	m.syncer.discovery = &blockTreeDiscovery{}
	got, err := m.syncer.trackedBlockPaths(ctx, repo, directory)
	if err != nil || !reflect.DeepEqual(got, []string{name, name}) {
		t.Fatalf("unmerged fallback: %q %v", got, err)
	}
}

func TestBlockTreeDiscoveryStillValidatesWorkingBytes(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "tree-validation", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "tree-validation", "Validate bytes after membership reuse")
	m.sync(t, OutcomeSynced)
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.syncer.discovery = &blockTreeDiscovery{}
	blocks, err := m.syncer.trackedBlocks(ctx, repo, testLineage)
	if err != nil || len(blocks) == 0 {
		t.Fatalf("initial inventory: %d %v", len(blocks), err)
	}
	directory, _ := recordsync.BlockDirectory(testLineage)
	name := filepath.Join(m.syncer.Clone, directory, blocks[0].Hash+".block")
	if err := os.WriteFile(name, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.syncer.trackedBlocks(ctx, repo, testLineage); err == nil {
		t.Fatal("tree cache concealed corrupt working bytes")
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.syncer.trackedBlocks(ctx, repo, testLineage); err == nil {
		t.Fatal("tree cache concealed missing tracked file")
	}
}
