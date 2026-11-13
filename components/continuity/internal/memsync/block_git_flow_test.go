package memsync

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// Exercise publication, provenance and semantic import through a disposable
// Git remote using committed objects and no index or checked-out records.
func TestBareBlockPublicationFlowThroughGit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "block-git-a", remote), newMachine(t, "block-git-b", remote)
	aa, ba := archiveMachine(t, &a), archiveMachine(t, &b)
	ai := recordsync.Index{DB: a.db, ProjectID: a.project, Lineage: testLineage}
	bi := recordsync.Index{DB: b.db, ProjectID: b.project, Lineage: testLineage}
	endpoint := "git:test"
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
		repo, err := m.syncer.repository(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := commitBlocks(ctx, repo, testLineage, blocks, "fixture: publish blocks", false); err != nil {
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
		if _, err := m.syncer.readTrackedBlocks(ctx, repo, testLineage, false, func(hash string, body []byte) (recordsync.Block, error) {
			return index.ReadPublicationBytes(ctx, hash, body, archive, endpoint)
		}); err != nil {
			t.Fatal(err)
		}
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
	fixtureFetchMerge(t, bRepo, "main")
	read(b, ba, bi)
	b.decide(t, "from-b", "Git block B")
	write(b, ba, bi)
	aRepo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFetchMerge(t, aRepo, "main")
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
