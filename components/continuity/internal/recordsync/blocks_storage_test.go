package recordsync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestBlockPublicationAndIncompleteScan(t *testing.T) {
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	if added, err := PublishBlock(root, lineage, block); err != nil || !added {
		t.Fatalf("publish: added=%v err=%v", added, err)
	}
	if added, err := PublishBlock(root, lineage, block); err != nil || added {
		t.Fatalf("replay: added=%v err=%v", added, err)
	}
	base, err := BlockDirectory(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, base, Hash("incomplete")+".block"), block.Bytes[:len(block.Bytes)-1], 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, base, "provider-conflict.block"), block.Bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, base, block.Hash+".block"), filepath.Join(directory, base, Hash("symlink")+".block")); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanBlocks(context.Background(), root, lineage)
	if err != nil || len(scan.Blocks) != 1 || scan.Pending != 2 || scan.Invalid != 1 || !bytes.Equal(scan.Blocks[0].Bytes, block.Bytes) {
		t.Fatalf("unsafe or incomplete scan: %+v %v", scan, err)
	}
}

func TestAuthorizedBlocksRespectRecordOrigins(t *testing.T) {
	ctx := context.Background()
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	local := fixture(t)
	foreign, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "foreign", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(local, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(foreign, "folder:source"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		endpoint string
		relay    bool
		count    int
	}{{"git:other", false, 1}, {"folder:source", false, 2}, {"git:other", true, 2}} {
		blocks, err := archive.AuthorizedBlocks(ctx, tc.endpoint, tc.relay)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := archive.AuthorizedBlocks(ctx, tc.endpoint, tc.relay)
		if err != nil || len(repeated) != len(blocks) {
			t.Fatalf("repeat packing: %v", err)
		}
		for i := range blocks {
			if repeated[i].Hash != blocks[i].Hash || !bytes.Equal(repeated[i].Bytes, blocks[i].Bytes) {
				t.Fatal("packing the same set changed its blocks")
			}
		}
		var objects []portable.EncodedEnvelope
		for _, block := range blocks {
			objects = append(objects, block.Objects...)
		}
		if len(objects) != tc.count {
			t.Fatalf("endpoint %s relay=%v: got %d records", tc.endpoint, tc.relay, len(objects))
		}
		if tc.count == 1 && objects[0].Envelope.EnvelopeSHA256 != local.Envelope.EnvelopeSHA256 {
			t.Fatal("unauthorized record was packed for another endpoint")
		}
	}
}
