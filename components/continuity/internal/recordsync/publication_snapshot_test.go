package recordsync

import (
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestPublicationSnapshotPreservesIndexedMembership(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "snapshot-growth")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	for cycle := 0; cycle < 4; cycle++ {
		count := 1
		if cycle == 0 {
			count = 500
		}
		for i := 0; i < count; i++ {
			e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordRevision: 1, RecordID: fmt.Sprintf("%d-%d", cycle, i), RecordedAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Payload: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = archive.Put(e, "local"); err != nil {
				t.Fatal(err)
			}
		}
		published, snapshot, err := index.PlanPublicationWithInventory(ctx, archive, "git:destination", false, nil)
		if err != nil {
			t.Fatal(err)
		}
		var before, after, unique, blocks int
		if err = db.QueryRow(`SELECT COUNT(*) FROM history_index_records`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err = index.blockObjects(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT envelope_hash) FROM history_index_records`).Scan(&after, &unique); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT COUNT(*) FROM history_index_blocks`).Scan(&blocks); err != nil {
			t.Fatal(err)
		}
		if before != after || after != unique || unique != 500+cycle || blocks != 1+cycle {
			t.Fatalf("snapshot duplicated indexed history: before=%d after=%d unique=%d blocks=%d", before, after, unique, blocks)
		}
		t.Logf("cycle=%d publication_blocks=%d snapshot_blocks=%d rows_before=%d rows_after=%d added_by_snapshot=%d unique=%d indexed_blocks=%d", cycle, len(published), len(snapshot), before, after, after-before, unique, blocks)
	}
}
