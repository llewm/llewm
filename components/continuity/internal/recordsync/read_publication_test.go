package recordsync

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestReadPublicationChecksInventoryOnceAndRepairs(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "read-publication")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "dependent", RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`), Refs: []portable.Reference{{Kind: "decision", ID: "target"}}})
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := PublishBlock(root, lineage, block); err != nil {
		t.Fatal(err)
	}
	path, err := blockPath(lineage, block.Hash)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	endpoint := "git:observed"
	for _, damage := range []string{
		"",
		`UPDATE history_index_envelopes SET envelope_json='{}'`,
		`DELETE FROM history_index_refs`,
		`DELETE FROM history_index_envelopes`,
		`UPDATE history_index_blocks SET index_version=0`,
	} {
		t.Run(damage, func(t *testing.T) {
			if damage != "" {
				if _, err := db.ExecContext(ctx, damage); err != nil {
					t.Fatal(err)
				}
			}
			measured, metrics := syncwork.WithWorkMetrics(ctx)
			got, err := index.ReadPublication(measured, root, path, archive, endpoint)
			if err != nil || !bytes.Equal(got.Bytes, block.Bytes) || !got.matchesValidatedContents(lineage) {
				t.Fatalf("read observation lost exact snapshot: %+v %v", got, err)
			}
			counts := metrics.Counts()
			if damage == "" && counts["inventory_records_checked"] != 1 {
				t.Fatalf("healthy observation repeated inventory verification: %v", counts)
			}
			if damage != "" && counts["fallback_block_inventory_repair"] != 1 {
				t.Fatalf("damaged inventory was not repaired: %v", counts)
			}
			if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
				t.Fatalf("observation left damaged inventory: %v", err)
			}
			var memberships int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND endpoint_hash=?`, project, Hash(endpoint)).Scan(&memberships); err != nil || memberships != 1 {
				t.Fatalf("observation membership: %d %v", memberships, err)
			}
			retained, err := archive.Objects(ctx, "", true)
			if err != nil || len(retained) != 1 || !bytes.Equal(retained[0].Bytes, object.Bytes) {
				t.Fatalf("observation did not retain exact envelope: %+v %v", retained, err)
			}
			if _, err := os.Stat(filepath.Join(archive.Directory, "origins", object.Envelope.EnvelopeSHA256, Hash(endpoint))); err != nil {
				t.Fatalf("observation lost endpoint provenance: %v", err)
			}
			// Publication provenance is required even for a warm indexed read;
			// observation alone must never authorize delivery to another endpoint.
			planned, err := index.PlanPublication(ctx, archive, "git:other", false)
			if err != nil || len(planned) != 0 {
				t.Fatalf("observed foreign record authorized relay: %+v %v", planned, err)
			}
			got.Objects[0].Bytes[0] ^= 1
			objects, err := index.importBlockObjects(ctx, []Block{got})
			if err != nil || len(objects) != 1 || !bytes.Equal(objects[0].Bytes, object.Bytes) {
				t.Fatalf("caller mutation escaped private snapshot: %+v %v", objects, err)
			}
		})
	}
	// No snapshot or membership is returned if provenance recording fails.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_publication BEFORE INSERT ON history_index_publications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if got, err := index.ReadPublication(ctx, root, path, archive, "git:failed"); err == nil || got.Hash != "" {
		t.Fatalf("failed observation exposed snapshot: %+v %v", got, err)
	}
}
