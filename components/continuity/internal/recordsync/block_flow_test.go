package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

// Exercise the experimental publication primitives across two local peers.
// This is a disposable folder fixture, not the live folder-sync implementation.
func TestBlockFlowAcrossTwoPeers(t *testing.T) {
	ctx, aDB, aID := indexedTestProject(t, "block-flow-a")
	_, bDB, bID := indexedTestProject(t, "block-flow-b")
	_, referenceDB, referenceID := indexedTestProject(t, "block-flow-reference")
	aArchive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	bArchive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	aIndex := Index{DB: aDB, ProjectID: aID, Lineage: lineage}
	bIndex := Index{DB: bDB, ProjectID: bID, Lineage: lineage}
	endpoint := "folder:test"
	appendRecord := func(db *sql.DB, project, id string) {
		t.Helper()
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: id, ProjectID: project, Title: id, Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "A peer observation", Authority: "caller_asserted"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	cycle := func(index Index, archive Archive) {
		t.Helper()
		local, err := portable.EncodeMemoryExport(ctx, index.DB, index.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		if err := archive.CaptureLocal(local.Envelopes); err != nil {
			t.Fatal(err)
		}
		scan, err := ScanBlocks(ctx, root, lineage)
		if err != nil || scan.Pending+scan.Invalid != 0 {
			t.Fatalf("scan: %+v %v", scan, err)
		}
		for _, block := range scan.Blocks {
			if err := index.ObservePublication(ctx, archive, endpoint, block); err != nil {
				t.Fatal(err)
			}
		}
		outgoing, err := index.PlanPublication(ctx, archive, endpoint, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range outgoing {
			if _, err := PublishBlock(root, lineage, block); err != nil {
				t.Fatal(err)
			}
		}
		all, err := index.PlanPublication(ctx, archive, "internal:semantic", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := index.ImportBlocks(ctx, all); err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(aDB, aID, "from-a")
	cycle(aIndex, aArchive)
	cycle(bIndex, bArchive)
	appendRecord(bDB, bID, "from-b")
	cycle(bIndex, bArchive)
	cycle(aIndex, aArchive)
	cycle(bIndex, bArchive)
	objects, err := aArchive.Objects(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := (Importer{DB: referenceDB, ProjectID: referenceID}).Import(ctx, objects); err != nil || result.ImportedRecords != 2 {
		t.Fatalf("reference import: %+v %v", result, err)
	}
	canonical := func(db *sql.DB, project string) [][]byte {
		t.Helper()
		export, err := portable.EncodeMemoryExport(ctx, db, project)
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
	want := canonical(referenceDB, referenceID)
	for _, peer := range []struct {
		db *sql.DB
		id string
	}{{aDB, aID}, {bDB, bID}} {
		got := canonical(peer.db, peer.id)
		if len(got) != len(want) || len(got) != 2 {
			t.Fatalf("peer record counts differ: %d %d", len(got), len(want))
		}
		for i := range want {
			if !bytes.Equal(want[i], got[i]) {
				t.Fatalf("peer record %d differs from reference", i)
			}
		}
	}
}
