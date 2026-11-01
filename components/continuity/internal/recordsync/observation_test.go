package recordsync

import (
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"testing"
)

func TestObservationBatchAtomicallyBindsEpochInventoryAndVerifiedBytes(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	object := archiveObject(t, "original", `{"value":1}`)
	block, err := EncodeBlock(store.Lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	stamp, accepted, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil || !accepted || stamp.Inventory == "" {
		t.Fatalf("complete: %+v %v %v", stamp, accepted, err)
	}
	// Canonical and local archive additions are independent from inventory damage.
	if _, err := store.DB.Exec(`UPDATE projects SET name='canonical change' WHERE id=?`, store.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := archive.CaptureLocal([]portable.EncodedEnvelope{archiveObject(t, "local", `{"value":2}`)}); err != nil {
		t.Fatal(err)
	}
	current, err := index.ObservationStamp(ctx, archive)
	if err != nil || current != stamp {
		t.Fatalf("append invalidated immutable observation: %+v %v", current, err)
	}
	if next, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", stamp, false, nil); err != nil || !ok || next != stamp {
		t.Fatalf("reuse: %+v %v %v", next, ok, err)
	}
	if _, err := store.DB.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, store.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", stamp, false, nil); err != nil || ok {
		t.Fatalf("stale observation accepted: %v %v", ok, err)
	}
	repaired, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil || !ok || repaired == stamp {
		t.Fatalf("repair: %+v %v %v", repaired, ok, err)
	}
	if _, err := index.ReadBlockBytes(ctx, block.Hash, block.Bytes); err != nil {
		t.Fatalf("repair did not restore inventory: %v", err)
	}
	if audit, err := archive.AuditStorage(ctx); err != nil || audit.Envelopes != 2 || audit.Origins != 2 {
		t.Fatalf("archive: %+v %v", audit, err)
	}
}

func TestObservationBatchRollsBackIndexAndArchiveTogether(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	block, err := EncodeBlock(store.Lineage, []portable.EncodedEnvelope{archiveObject(t, "rollback", `{"value":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`CREATE TRIGGER fixture_origin_failure BEFORE INSERT ON history_archive_origins BEGIN SELECT RAISE(ABORT,'fixture origin failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err == nil || ok {
		t.Fatal("accepted failed observation")
	}
	for _, table := range []string{"history_index_blocks", "history_index_envelopes", "history_index_publications", "history_archive_envelopes", "history_archive_origins"} {
		var count int
		if err := store.DB.QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id=?`, store.ProjectID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s leaked %d rows: %v", table, count, err)
		}
	}
}

func TestMissingObservationTrackingUsesCompleteValidationWithoutReusableStamp(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	before, err := index.ObservationStamp(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`DROP TRIGGER history_inventory_history_index_blocks_insert`); err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(store.Lineage, []portable.EncodedEnvelope{archiveObject(t, "source", `{"value":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", before, false, []Block{block}); err != nil || ok {
		t.Fatalf("accepted incomplete tracking: %v %v", ok, err)
	}
	after, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil || !ok || after != (ObservationStamp{}) {
		t.Fatalf("complete fallback: %+v %v %v", after, ok, err)
	}
	if _, err := archive.AuditStorage(ctx); err != nil {
		t.Fatal(err)
	}
	// A borrowed parsed object cannot override the bytes being observed.
	block.Objects[0].Envelope.RecordID = "borrowed"
	if _, _, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err != nil {
		t.Fatal(err)
	}
}

func TestObservedPublicationAdvancesOnlyItsOwnVerifiedInventoryWrites(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	original := archiveObject(t, "original", `{"value":1}`)
	block, err := EncodeBlock(store.Lineage, []portable.EncodedEnvelope{original})
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.CaptureLocal([]portable.EncodedEnvelope{archiveObject(t, "new", `{"value":2}`)}); err != nil {
		t.Fatal(err)
	}
	planned, snapshot, after, err := index.PlanObservedPublicationWithInventory(ctx, archive, "git:fixture", false, before)
	if err != nil || len(planned) != 2 || len(snapshot) != 2 || after.Inventory == "" || after == before || after.ArchiveEpoch != before.ArchiveEpoch {
		t.Fatalf("own writes: plan=%d snapshot=%d after=%+v: %v", len(planned), len(snapshot), after, err)
	}
	current, err := index.ObservationStamp(ctx, archive)
	if err != nil || current != after {
		t.Fatalf("wrong transaction receipt: %+v %v", current, err)
	}
	if _, _, stale, err := index.PlanObservedPublicationWithInventory(ctx, archive, "git:fixture", false, before); err != nil || stale != (ObservationStamp{}) {
		t.Fatalf("blessed stale inventory: %+v %v", stale, err)
	}
	// A catalog edit during the archive scan leaves block membership unchanged.
	// The inventory generation must still prevent receipt advancement.
	tracking := &publicationTracking{expected: after, valid: true}
	_, err = index.planPublicationWithScanRecovery(ctx, archive, "git:fixture", false, nil, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
		if _, err := store.DB.Exec(`UPDATE history_index_envelopes SET record_id='concurrent' WHERE project_id=?`, store.ProjectID); err != nil {
			return nil, nil, err
		}
		return archive.objects(ctx, "git:fixture", false)
	}, tracking)
	if err != nil || tracking.valid {
		t.Fatalf("concurrent inventory was blessed: %v %v", tracking.valid, err)
	}
}

func TestMissingArchiveCounterTrackingCannotCommitObservation(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	if _, err := store.DB.Exec(`DROP TRIGGER history_archive_change_insert`); err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(store.Lineage, []portable.EncodedEnvelope{archiveObject(t, "untracked", `{"value":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err == nil || ok {
		t.Fatal("accepted missing archive journal counters")
	}
	if audit, err := archive.AuditStorage(ctx); err != nil || audit.Envelopes != 0 {
		t.Fatalf("failed observation leaked data: %+v %v", audit, err)
	}
}
