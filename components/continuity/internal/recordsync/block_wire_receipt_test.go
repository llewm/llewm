package recordsync

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func assertWireReceiptReference(t testing.TB, block Block, selectedLineage string) {
	t.Helper()
	want, wantErr := referenceDecodeBlock(selectedLineage, block.Hash, block.Bytes)
	got, err := decodeObservedBlock(context.Background(), selectedLineage, block)
	if (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() {
		t.Fatalf("wire receipt acceptance/order: %v vs %v", err, wantErr)
	}
	if got.validated != nil {
		t.Fatal("wire receipt became a database snapshot proof")
	}
	got.decoded = nil
	if err == nil && !reflect.DeepEqual(got, want) {
		t.Fatal("wire receipt changed decoded view")
	}
}

func TestWireReceiptIgnoresMutableExportedFieldsAndOwnsResults(t *testing.T) {
	block, _ := matchingIndexBlocks(t, 1)
	if block.decoded == nil || block.validated != nil {
		t.Fatal("wrong receipt scope")
	}
	want, err := referenceDecodeBlock(lineage, block.Hash, block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	block.Objects[0].Bytes[0] = '['
	block.Objects[0].Envelope.RecordID = "forged"
	block.Objects[0].Envelope.Payload[0] = '['
	block.Objects[0].Envelope.Refs[0].ID = "forged"
	block.Objects = block.Objects[:1]
	ctx, work := syncwork.WithWorkMetrics(context.Background())
	for range 2 {
		got, err := decodeObservedBlock(ctx, lineage, block)
		if err != nil {
			t.Fatal(err)
		}
		if got.validated != nil {
			t.Fatal("transport proof certified database state")
		}
		got.decoded = nil
		if !reflect.DeepEqual(got, want) {
			t.Fatal("trusted exported metadata or aliased output")
		}
		got.Bytes[0] = '['
		got.Objects[0].Bytes[0] = '['
		got.Objects[0].Envelope.Payload[0] = '['
		got.Objects[0].Envelope.Refs[0].ID = "output mutation"
	}
	if work.Counts()["block_wire_rechecks_reused"] != 2 {
		t.Fatal(work.Counts())
	}
}

func TestWireReceiptRetainsFullDecoderChecksAndErrorOrder(t *testing.T) {
	base, _ := matchingIndexBlocks(t, 1)
	replacement, err := EncodeBlock(lineage, []portable.EncodedEnvelope{archiveObject(t, "other", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{base.Bytes, replacement.Bytes, nil, append(bytes.Clone(base.Bytes), '\n'), base.Bytes[:len(base.Bytes)-1], []byte(strings.Repeat("x", MaxBlockBytes+1))} {
		for _, hash := range []string{"", base.Hash, replacement.Hash, "forged"} {
			for _, selectedLineage := range []string{lineage, "different"} {
				block := base
				block.Bytes, block.Hash, block.Objects = body, hash, nil
				assertWireReceiptReference(t, block, selectedLineage)
				block.decoded = nil
				assertWireReceiptReference(t, block, selectedLineage)
			}
		}
	}
}

func TestWireReceiptParallelViewsRemainIsolated(t *testing.T) {
	block, _ := matchingIndexBlocks(t, 1)
	var workers sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 8 {
				got, err := decodeObservedBlock(context.Background(), lineage, block)
				if err != nil {
					errors <- err
					return
				}
				got.Bytes[0] = '['
				got.Objects[0].Bytes[0] = '['
				got.Objects[0].Envelope.Payload[0] = '['
				got.Objects[0].Envelope.Refs[0].ID = "changed"
			}
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	assertWireReceiptReference(t, block, lineage)
}

func TestWireReceiptDoesNotHideDamagedDatabaseInventory(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "wire-is-not-db")
	block, _ := matchingIndexBlocks(t, 1)
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := index.importBlockObjects(ctx, []Block{block}); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("wire receipt bypassed fresh inventory: %v", err)
	}
}

func TestWireReceiptObservationStillRepairsAndAuditsDatabase(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	block, _ := matchingIndexBlocks(t, 1)
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:wire", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
		t.Fatalf("initial observation: %v %v", ok, err)
	}
	if _, err := store.DB.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, store.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, work := syncwork.WithWorkMetrics(ctx)
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:wire", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
		t.Fatalf("repair observation: %v %v", ok, err)
	}
	counts := work.Counts()
	if counts["block_wire_rechecks_reused"] != 1 || counts["archive_audit_envelopes"] != int64(len(block.Objects)) || counts["inventory_match_records"] == 0 {
		t.Fatalf("fresh database checks missing: %v", counts)
	}
	if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
		t.Fatal(err)
	}
}

func FuzzWireReceiptMatchesFullDecoder(f *testing.F) {
	base, _ := matchingIndexBlocks(f, 1)
	for _, body := range [][]byte{base.Bytes, []byte("{}"), nil, append(bytes.Clone(base.Bytes), '\n')} {
		f.Add(body, base.Hash)
	}
	f.Fuzz(func(t *testing.T, body []byte, hash string) {
		block := base
		block.Bytes, block.Hash, block.Objects = body, hash, nil
		assertWireReceiptReference(t, block, lineage)
	})
}
