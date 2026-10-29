package recordsync

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestPublicationSnapshotPreservesIndexedMembership(t *testing.T) {
	for _, origin := range []string{"local", "folder:independent"} {
		t.Run(origin, func(t *testing.T) { testPublicationSnapshotPreservesIndexedMembership(t, origin) })
	}
}

func TestForeignSnapshotRecoveryAndOriginChange(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "foreign-snapshot-recovery")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordRevision: 1, RecordID: "foreign", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`), Refs: []portable.Reference{{Kind: "decision", ID: "target"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(object, "folder:independent"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	plan := func() []Block {
		t.Helper()
		outbound, snapshot, err := index.PlanPublicationWithInventory(ctx, archive, "git:destination", false, nil)
		if err != nil || len(outbound) != 0 || len(snapshot) != 1 || len(snapshot[0].Objects) != 1 || !bytes.Equal(snapshot[0].Objects[0].Bytes, object.Bytes) {
			t.Fatalf("foreign snapshot: outbound=%+v snapshot=%+v err=%v", outbound, snapshot, err)
		}
		return snapshot
	}
	initial := plan()
	for _, damage := range []string{
		`UPDATE history_index_records SET envelope_json='{}'`,
		`UPDATE history_index_blocks SET index_version=0`,
	} {
		if _, err := db.Exec(damage); err != nil {
			t.Fatal(err)
		}
		recovered := plan()
		if recovered[0].Hash != initial[0].Hash {
			t.Fatal("repair changed foreign snapshot identity")
		}
	}
	if _, err := db.Exec(`DELETE FROM history_index_refs`); err != nil {
		t.Fatal(err)
	}
	if _, err := index.blockObjects(ctx, plan()); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("damaged reference inventory trusted: %v", err)
	}
	if err := index.RepairBlock(ctx, initial[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := index.blockObjects(ctx, plan()); err != nil {
		t.Fatal(err)
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if rebuilt := plan(); rebuilt[0].Hash != initial[0].Hash {
		t.Fatal("archive rebuild changed snapshot identity")
	}
	// Adding local provenance may authorize this exact object for delivery.
	// Local snapshot membership alone must never do so.
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	outbound, snapshot, err := index.PlanPublicationWithInventory(ctx, archive, "git:destination", false, nil)
	if err != nil || len(outbound) != 1 || len(snapshot) != 1 || outbound[0].Hash != initial[0].Hash || snapshot[0].Hash != initial[0].Hash {
		t.Fatalf("origin promotion lost exact record: %+v %+v %v", outbound, snapshot, err)
	}
}

func testPublicationSnapshotPreservesIndexedMembership(t *testing.T, origin string) {
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
			if _, err = archive.Put(e, origin); err != nil {
				t.Fatal(err)
			}
		}
		published, snapshot, err := index.PlanPublicationWithInventory(ctx, archive, "git:destination", false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if origin != "local" && len(published) != 0 {
			t.Fatal("foreign snapshot membership authorized outbound publication")
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
