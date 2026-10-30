//go:build linux

package recordsync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestManifestCoveragePersistsAndValidatesWorkingBytes(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "coverage")
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	block := coverageBlock(t, "first")
	if _, err := PublishBlock(root, lineage, block); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PublishManifest(ctx, root, lineage, []string{block.Hash}); err != nil {
		t.Fatal(err)
	}
	assertScan := func(reused bool, pending int) {
		t.Helper()
		got, err := index.ScanBlocksWithCatalog(ctx, root, archive, "folder:one")
		want, fullErr := ScanBlocks(ctx, root, lineage)
		if err != nil || fullErr != nil || got.CatalogReused != reused || got.Pending != pending || got.Invalid != want.Invalid || len(got.Blocks) != len(want.Blocks) {
			t.Fatalf("scan differs: got=%+v want=%+v errors=%v/%v", got, want, err, fullErr)
		}
		for i, block := range got.Blocks {
			if block.Hash != want.Blocks[i].Hash || !bytes.Equal(block.Bytes, want.Blocks[i].Bytes) {
				t.Fatal("catalog scan changed observations")
			}
		}
	}
	assertScan(false, 0)
	// No in-memory receipt survives reopening the transport and local archive.
	root.Close()
	root, err = OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	archive, err = OpenArchive(archive.Directory, lineage)
	if err != nil {
		t.Fatal(err)
	}
	index = Index{DB: db, ProjectID: project, Lineage: lineage}
	assertScan(true, 0)
	// A receipt must not hide disposable index corruption.
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	assertScan(true, 0)
	// Editing an existing file leaves directory membership unchanged. Old bytes
	// still have to be read and rejected, even if the mtime is restored.
	path, _ := blockPath(lineage, block.Hash)
	info, err := root.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), block.Bytes...)
	damaged[len(damaged)-2] ^= 1
	if err := os.WriteFile(filepath.Join(directory, path), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(directory, path), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	assertScan(true, 1)
}

func TestManifestCoverageNeverHidesUnannouncedOrInvalidEntries(t *testing.T) {
	for _, mutation := range []string{"unannounced", "partial", "conflict", "deleted", "symlink", "catalog-damaged", "new-root", "receipt-damaged"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, db, project := indexedTestProject(t, mutation)
			index := Index{DB: db, ProjectID: project, Lineage: lineage}
			archive, err := OpenArchive(t.TempDir(), lineage)
			if err != nil {
				t.Fatal(err)
			}
			root, err := OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			first, second := coverageBlock(t, "first"), coverageBlock(t, "second")
			if _, err := PublishBlock(root, lineage, first); err != nil {
				t.Fatal(err)
			}
			hash, _, err := PublishManifest(ctx, root, lineage, []string{first.Hash})
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				got, err := index.ScanBlocksWithCatalog(ctx, root, archive, "folder:one")
				if err != nil || got.CatalogReused != (n == 1) {
					t.Fatalf("establish receipt: %+v %v", got, err)
				}
			}
			base, _ := BlockDirectory(lineage)
			path, _ := blockPath(lineage, first.Hash)
			switch mutation {
			case "unannounced":
				_, err = PublishBlock(root, lineage, second)
			case "partial":
				_, err = WriteFile(root, filepath.Join(base, Hash("partial")+".block"), []byte("partial"), false)
			case "conflict":
				_, err = WriteFile(root, filepath.Join(base, "provider-conflict.block"), first.Bytes, false)
			case "deleted":
				err = root.Remove(path)
			case "symlink":
				err = root.Symlink(first.Hash+".block", filepath.Join(base, Hash("symlink")+".block"))
			case "catalog-damaged":
				path, _ := manifestPath(lineage, hash)
				_, err = WriteFile(root, path, []byte("damaged"), true)
			case "new-root":
				_, err = PublishBlock(root, lineage, second)
				if err == nil {
					_, _, err = PublishManifest(ctx, root, lineage, []string{second.Hash})
				}
			case "receipt-damaged":
				key := "manifest-coverage:" + Hash(project+"\x00"+lineage+"\x00folder:one")
				err = archive.SaveStatus(key, map[string]any{"version": 999})
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := index.ScanBlocksWithCatalog(ctx, root, archive, "folder:one")
			want, fullErr := ScanBlocks(ctx, root, lineage)
			if err != nil || fullErr != nil || got.CatalogReused || got.Pending != want.Pending || got.Invalid != want.Invalid || len(got.Blocks) != len(want.Blocks) {
				t.Fatalf("mutation hidden: got=%+v want=%+v errors=%v/%v", got, want, err, fullErr)
			}
			for i := range got.Blocks {
				if !bytes.Equal(got.Blocks[i].Bytes, want.Blocks[i].Bytes) {
					t.Fatal("mutation changed received observations")
				}
			}
			// A valid but incomplete union must never mint a replacement proof.
			retry, err := index.ScanBlocksWithCatalog(ctx, root, archive, "folder:one")
			canCover := mutation == "new-root" || mutation == "receipt-damaged"
			if err != nil || retry.CatalogReused != canCover {
				t.Fatalf("incorrect replacement proof: %+v %v", retry, err)
			}
		})
	}
}

func TestManifestCoverageIsScopedAndCancellationIsNotCompletion(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "scope")
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	base, _ := BlockDirectory(lineage)
	if err := root.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PublishManifest(ctx, root, lineage, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		endpoint string
		reused   bool
	}{{"one", false}, {"two", false}, {"one", true}} {
		got, err := index.ScanBlocksWithCatalog(ctx, root, archive, tc.endpoint)
		if err != nil || got.CatalogReused != tc.reused {
			t.Fatalf("scope: %+v %v", got, err)
		}
	}
	other := index
	other.ProjectID = "another-project"
	if got, err := other.ScanBlocksWithCatalog(ctx, root, archive, "one"); err != nil || got.CatalogReused {
		t.Fatalf("another project reused receipt: %+v %v", got, err)
	}
	if got, err := index.ScanBlocksWithCatalog(ctx, root, archive, "one"); err != nil || !got.CatalogReused {
		t.Fatalf("empty covered directory: %+v %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := index.ScanBlocksWithCatalog(cancelled, root, archive, "one"); err != context.Canceled || !reflect.DeepEqual(got, BlockScanResult{}) {
		t.Fatalf("cancelled scan: %+v %v", got, err)
	}
}

// With one announced leaf, the eighth cancellation check is the first block
// read, after root discovery and before the final directory identity check.
// Mutate there without a timing-dependent goroutine or production fault hook.
type coveragePublicationContext struct {
	context.Context
	checks atomic.Int32
	add    func()
}

func (c *coveragePublicationContext) Err() error {
	if c.checks.Add(1) == 8 {
		c.add()
	}
	return c.Context.Err()
}

func TestManifestCoverageRejectsConcurrentUnannouncedPublication(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(map[bool]string{false: "establish", true: "reuse"}[saved], func(t *testing.T) {
			ctx, db, project := indexedTestProject(t, "concurrent-coverage")
			index := Index{DB: db, ProjectID: project, Lineage: lineage}
			archive, err := OpenArchive(t.TempDir(), lineage)
			if err != nil {
				t.Fatal(err)
			}
			root, err := OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			first, second := coverageBlock(t, "first"), coverageBlock(t, "second")
			if _, err := PublishBlock(root, lineage, first); err != nil {
				t.Fatal(err)
			}
			if _, _, err := PublishManifest(ctx, root, lineage, []string{first.Hash}); err != nil {
				t.Fatal(err)
			}
			if saved {
				if _, err := index.ScanBlocksWithCatalog(ctx, root, archive, "one"); err != nil {
					t.Fatal(err)
				}
			}
			added := false
			racing := &coveragePublicationContext{Context: ctx, add: func() {
				added = true
				if _, err := PublishBlock(root, lineage, second); err != nil {
					t.Fatal(err)
				}
			}}
			got, err := index.ScanBlocksWithCatalog(racing, root, archive, "one")
			if !added || err != nil || got.CatalogReused || (saved && len(got.Blocks) != 2) {
				t.Fatalf("publication race hidden: added=%v got=%+v err=%v", added, got, err)
			}
			// Recovery must find the unannounced block and must not treat the
			// old, incomplete catalog as complete on subsequent attempts.
			for attempt := 0; attempt < 2; attempt++ {
				got, err := index.ScanBlocksWithCatalog(ctx, root, archive, "one")
				if err != nil || got.CatalogReused || len(got.Blocks) != 2 {
					t.Fatalf("retry lost arrival: %+v %v", got, err)
				}
			}
		})
	}
}

func coverageBlock(t *testing.T, id string) Block {
	t.Helper()
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordRevision: 1, RecordedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	return block
}
