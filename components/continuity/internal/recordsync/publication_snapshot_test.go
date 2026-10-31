package recordsync

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestPublicationReusesOnlyPermittedExactEnvelopes(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-envelope-reuse")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	if _, err := archive.Put(object, "folder:independent"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	const endpoint = "git:destination"
	initial, err := index.PlanPublication(ctx, archive, endpoint, true)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial relay: %+v %v", initial, err)
	}
	for _, relay := range []bool{true, false} {
		measured, metrics := syncwork.WithWorkMetrics(ctx)
		blocks, err := index.PlanPublication(measured, archive, endpoint, relay)
		counts := metrics.Counts()
		if err != nil {
			t.Fatal(err)
		}
		if relay {
			if len(blocks) != 1 || blocks[0].Hash != initial[0].Hash || !bytes.Equal(blocks[0].Bytes, initial[0].Bytes) || !blocks[0].matchesValidatedContents(lineage) || counts["publication_envelopes_reused"] != 1 || counts["publication_envelope_decodes"] != 0 {
				t.Fatalf("exact snapshot not reused: %+v %v", blocks, counts)
			}
		} else if len(blocks) != 0 || counts["publication_envelopes_reused"] != 0 || counts["publication_envelope_decodes"] != 1 {
			t.Fatalf("restricted envelope reused: %+v %v", blocks, counts)
		}
	}
	// A damaged entry is neither authorized by its hash column nor hidden by
	// removing relay permission. Use the planner without recovery to observe
	// rejection before the public path repairs it from the archive.
	if _, err := db.Exec(`UPDATE history_index_envelopes SET envelope_json='{}' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	for _, relay := range []bool{true, false} {
		_, err := index.planPublication(ctx, archive, endpoint, relay, nil, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
			return archive.objects(ctx, endpoint, relay)
		})
		if !errors.Is(err, ErrIndexRebuild) {
			t.Fatalf("damaged publication accepted (relay=%t): %v", relay, err)
		}
	}
	repaired, err := index.PlanPublication(ctx, archive, endpoint, true)
	if err != nil || len(repaired) != 1 || repaired[0].Hash != initial[0].Hash || !bytes.Equal(repaired[0].Bytes, initial[0].Bytes) {
		t.Fatalf("repair lost publication: %+v %v", repaired, err)
	}
}

func TestPublicationSnapshotPreservesIndexedMembership(t *testing.T) {
	for _, origin := range []string{"local", "folder:independent"} {
		t.Run(origin, func(t *testing.T) { testPublicationSnapshotPreservesIndexedMembership(t, origin) })
	}
}

func TestPublicationStreamsMixedBlocksAndRejectsLateDamage(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-stream")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	const endpoint = "git:destination"
	want := make(map[string][]byte)
	for i, origin := range []string{"local", "folder:independent", "local"} {
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: fmt.Sprintf("stream-%d", i), RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Put(object, origin); err != nil {
			t.Fatal(err)
		}
		// Each arrival becomes a separate immutable block. The foreign block
		// must not affect permission or coverage of its local neighbors.
		blocks, err := index.PlanPublication(ctx, archive, endpoint, true)
		if err != nil || len(blocks) != i+1 {
			t.Fatalf("stage %d: %d blocks, %v", i, len(blocks), err)
		}
		if origin == "local" {
			for _, block := range blocks {
				if len(block.Objects) == 1 && block.Objects[0].Envelope.RecordID == object.Envelope.RecordID {
					want[block.Hash] = append([]byte(nil), block.Bytes...)
				}
			}
		}
	}
	blocks, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(blocks) != 2 || len(want) != 2 {
		t.Fatalf("local-only plan: %d blocks, %v", len(blocks), err)
	}
	for _, block := range blocks {
		if !bytes.Equal(block.Bytes, want[block.Hash]) || !block.matchesValidatedContents(lineage) {
			t.Fatal("streaming changed block bytes, identity or permission")
		}
	}
	// Damage the final block in SQL order, after earlier blocks have already
	// been consumed. No partial plan or newly staged membership may escape.
	arrival, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "unstaged-arrival", RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(arrival, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM history_index_block_members WHERE project_id=? AND block_hash=(SELECT MAX(block_hash) FROM history_index_publications WHERE project_id=?)`, project, project); err != nil {
		t.Fatal(err)
	}
	got, err := index.planPublication(ctx, archive, endpoint, true, nil, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
		return archive.objects(ctx, endpoint, true)
	})
	if !errors.Is(err, ErrIndexRebuild) || len(got) != 0 {
		t.Fatalf("late damage returned a partial plan: %d blocks, %v", len(got), err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&count); err != nil || count != 3 {
		t.Fatalf("late damage changed publication membership: %d, %v", count, err)
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
		`UPDATE history_index_envelopes SET envelope_json='{}'`,
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
		if err = db.QueryRow(`SELECT COUNT(*) FROM history_index_envelopes`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err = index.blockObjects(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT envelope_hash) FROM history_index_envelopes`).Scan(&after, &unique); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT COUNT(*) FROM history_index_blocks`).Scan(&blocks); err != nil {
			t.Fatal(err)
		}
		if before != after || after != unique || unique != 500+cycle || blocks != 1+cycle {
			t.Fatalf("snapshot duplicated indexed history: before=%d after=%d unique=%d blocks=%d", before, after, unique, blocks)
		}
		// Both newly packed arrivals and reused publication membership must
		// carry an exact private snapshot, including foreign residual records.
		for _, block := range snapshot {
			if !block.matchesValidatedContents(lineage) {
				t.Fatal("publication snapshot cannot reuse verified envelopes")
			}
			want := append([]byte(nil), block.Objects[0].Bytes...)
			block.Objects[0].Bytes[0] ^= 1
			got, err := index.importBlockObjects(ctx, []Block{block})
			if err != nil || len(got) != len(block.Objects) || !bytes.Equal(got[0].Bytes, want) {
				t.Fatalf("publication parsed fields changed private snapshot: %v", err)
			}
		}
		t.Logf("cycle=%d publication_blocks=%d snapshot_blocks=%d rows_before=%d rows_after=%d added_by_snapshot=%d unique=%d indexed_blocks=%d", cycle, len(published), len(snapshot), before, after, after-before, unique, blocks)
	}
}
