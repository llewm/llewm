package recordsync

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestImportSnapshotPreservesPrivateContents(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "import-snapshot")
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, err = PublishBlock(root, lineage, block)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	path, err := blockPath(lineage, block.Hash)
	if err != nil {
		t.Fatal(err)
	}
	read, err := index.ReadBlock(ctx, root, path)
	if err != nil {
		t.Fatal(err)
	}
	// Exported parsed fields and returned objects must not alter the seal.
	read.Objects[0].Bytes[0] ^= 1
	read.Objects[0].Envelope.Payload[0] ^= 1
	read.Objects[0].Envelope.RecordID = "changed"
	want := block.Objects
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := index.ObservePublication(ctx, archive, "folder:source", read); err != nil || added != 1 {
		t.Fatalf("observe sealed block: %d %v", added, err)
	}
	observed, err := archive.Objects(ctx, "folder:source", false)
	if err != nil || !reflect.DeepEqual(observed, want) {
		t.Fatalf("observation trusted changed parsed fields: %v", err)
	}
	// A sealed import needs no second inventory read, even if that inventory
	// changed after the scan. The next scan remains responsible for repair.
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET record_id='damaged' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	got, err := index.importBlockObjects(ctx, []Block{read, read})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sealed snapshot: %v %v", got, err)
	}
	got[0].Bytes[0] ^= 1
	got[0].Envelope.Payload[0] ^= 1
	got, err = index.importBlockObjects(ctx, []Block{read})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("returned aliases changed seal: %v", err)
	}
	if _, err := index.ReadBlock(ctx, root, path); err != nil {
		t.Fatal(err)
	}
	if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
		t.Fatalf("fresh read did not repair inventory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := index.importBlockObjects(ctx, []Block{read}); err != nil {
		t.Fatalf("sealed extraction queried database: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := index.importBlockObjects(canceled, []Block{read}); err == nil {
		t.Fatal("canceled extraction succeeded")
	}
}

func TestImportSnapshotRequiresExactWireBytesAndLineage(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "import-snapshot-binding")
	first, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	// A different canonical observation supplies a valid replacement block.
	secondObject, err := portable.EncodeEnvelope(portable.EnvelopeSpec{
		LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1,
		RecordID: "replacement", RecordedAt: time.Unix(1234567890, 0),
		Authority: "caller_asserted", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeBlock(lineage, []portable.EncodedEnvelope{secondObject})
	if err != nil {
		t.Fatal(err)
	}
	first.validated = sealBlockContents(lineage, first.Objects)
	if !first.matchesValidatedContents(lineage) || first.matchesValidatedContents("other") {
		t.Fatal("seal lineage binding failed")
	}
	original := first
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	// Transplanting exported bytes/hash/objects must use the ordinary path,
	// which decodes the wire bytes rather than trusting either parsed view.
	first.Bytes, first.Hash = second.Bytes, second.Hash
	if first.matchesValidatedContents(lineage) {
		t.Fatal("old seal accepted replacement bytes")
	}
	got, err := index.importBlockObjects(ctx, []Block{first})
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Bytes, secondObject.Bytes) {
		t.Fatalf("replacement fallback: %v %v", got, err)
	}
	// A partial or extended copy of the original wire body cannot reuse it.
	for _, body := range [][]byte{original.Bytes[:len(original.Bytes)-1], append(append([]byte(nil), original.Bytes...), '\n')} {
		original.Bytes = body
		if original.matchesValidatedContents(lineage) {
			t.Fatal("nonexact wire bytes reused seal")
		}
		if _, err := index.ImportBlocks(ctx, []Block{original}); err == nil {
			t.Fatal("invalid block bytes imported")
		}
	}
}
