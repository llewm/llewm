package recordsync

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestIndexReadersDoNotHideNULTrailingBytes(t *testing.T) {
	for _, mode := range []string{"match", "catalog", "sequential_repair", "batch_repair"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, project := indexedTestProject(t, "index-nul-"+mode)
			block, _ := matchingIndexBlocks(t, 1)
			index := Index{DB: db, ProjectID: project, Lineage: lineage}
			if err := index.AddBlock(ctx, block); err != nil {
				t.Fatal(err)
			}
			object := block.Objects[0]
			if _, err := db.ExecContext(ctx, `UPDATE history_index_envelopes SET envelope_json=? WHERE project_id=? AND envelope_hash=?`, string(object.Bytes)+"\x00hidden", project, object.Envelope.EnvelopeSHA256); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mode {
			case "match":
				_, matched, err := index.blockObjectsMatchWithin(ctx, tx, block)
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					t.Fatal("accepted a canonical prefix with hidden trailing bytes")
				}
			case "catalog":
				if _, err := index.catalogBlockWithin(ctx, tx, block.Hash); !errors.Is(err, ErrIndexRebuild) {
					t.Fatalf("wanted rebuild for hidden bytes, got %v", err)
				}
			case "sequential_repair", "batch_repair":
				if mode == "sequential_repair" {
					err = index.insertBlockObjectsSequential(ctx, tx, block)
				} else {
					err = index.insertBlockObjects(ctx, tx, block)
				}
				if err != nil {
					t.Fatal(err)
				}
				var got []byte
				if err := tx.QueryRowContext(ctx, `SELECT envelope_json FROM history_index_envelopes WHERE project_id=? AND envelope_hash=?`, project, object.Envelope.EnvelopeSHA256).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, object.Bytes) {
					t.Fatal("repair retained hidden trailing bytes")
				}
			}
		})
	}
}

func TestIndexByteReadersPreserveLargeUnicodeEnvelope(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "index-unicode-bytes")
	object := archiveObject(t, "unicode", `{"text":"`+strings.Repeat("😀", 30<<10)+`"}`)
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if missing, matched, err := index.blockObjectsMatchWithin(ctx, tx, block); err != nil || !matched || len(missing) != 0 {
		t.Fatalf("valid Unicode match: %v %v %v", missing, matched, err)
	}
	got, err := index.catalogBlockWithin(ctx, tx, block.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes, block.Bytes) || len(got.Objects) != 1 || !bytes.Equal(got.Objects[0].Bytes, object.Bytes) {
		t.Fatal("Unicode bytes changed")
	}
}
