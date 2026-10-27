package memsync

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// Exercise the experimental block primitives through an actual disposable Git
// remote. The production Git sync path still uses record-per-file storage.
func TestExperimentalBlockFlowThroughGit(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "block-git-a", remote), newMachine(t, "block-git-b", remote)
	aa, ba := archiveMachine(t, &a), archiveMachine(t, &b)
	ai := recordsync.Index{DB: a.db, ProjectID: a.project, Lineage: testLineage}
	bi := recordsync.Index{DB: b.db, ProjectID: b.project, Lineage: testLineage}
	endpoint := "git:test"
	directory, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	write := func(m machine, archive recordsync.Archive, index recordsync.Index) {
		t.Helper()
		local, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
		if err != nil {
			t.Fatal(err)
		}
		if err := archive.CaptureLocal(local.Envelopes); err != nil {
			t.Fatal(err)
		}
		blocks, err := index.PlanPublication(ctx, archive, endpoint, false)
		if err != nil {
			t.Fatal(err)
		}
		root, err := recordsync.OpenRoot(m.syncer.Clone)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range blocks {
			if _, err := recordsync.PublishBlock(root, testLineage, block); err != nil {
				root.Close()
				t.Fatal(err)
			}
		}
		root.Close()
		repo, err := m.syncer.repository(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := commit(ctx, repo, directory, len(blocks), "fixture: publish blocks"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
			t.Fatal(err)
		}
	}
	read := func(m machine, archive recordsync.Archive, index recordsync.Index) {
		t.Helper()
		repo, err := m.syncer.repository(ctx)
		if err != nil {
			t.Fatal(err)
		}
		listing, err := repo.run(ctx, "ls-files", "--", directory)
		if err != nil {
			t.Fatal(err)
		}
		root, err := recordsync.OpenRoot(m.syncer.Clone)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range strings.Fields(listing) {
			block, err := recordsync.ReadBlock(root, testLineage, filepath.FromSlash(path))
			if err != nil {
				root.Close()
				t.Fatal(err)
			}
			if _, err := index.ObservePublication(ctx, archive, endpoint, block); err != nil {
				root.Close()
				t.Fatal(err)
			}
		}
		root.Close()
		all, err := index.PlanPublication(ctx, archive, "internal:semantic", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := index.ImportBlocks(ctx, all); err != nil {
			t.Fatal(err)
		}
	}
	a.decide(t, "from-a", "Git block A")
	write(a, aa, ai)
	bRepo, err := b.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bRepo.run(ctx, "fetch", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := bRepo.run(ctx, "checkout", "-B", "main", "origin/main"); err != nil {
		t.Fatal(err)
	}
	read(b, ba, bi)
	b.decide(t, "from-b", "Git block B")
	write(b, ba, bi)
	aRepo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aRepo.run(ctx, "fetch", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := aRepo.run(ctx, "rebase", "origin/main"); err != nil {
		t.Fatal(err)
	}
	read(a, aa, ai)
	canonical := func(m machine) [][]byte {
		t.Helper()
		export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
		if err != nil {
			t.Fatal(err)
		}
		var result [][]byte
		for _, object := range export.Envelopes {
			result = append(result, object.Bytes)
		}
		sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i], result[j]) < 0 })
		return result
	}
	x, y := canonical(a), canonical(b)
	if len(x) != 2 || len(y) != 2 {
		t.Fatalf("Git block peers did not converge: %d %d", len(x), len(y))
	}
	for i := range x {
		if !bytes.Equal(x[i], y[i]) {
			t.Fatalf("Git block peer record %d differs", i)
		}
	}
}
