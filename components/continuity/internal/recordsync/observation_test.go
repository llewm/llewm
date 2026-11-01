package recordsync

import (
	"database/sql"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestCompleteObservationAuditsUnobservedArchiveBodies(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	local := archiveObject(t, "local-only", `{"value":1}`)
	if err := archive.CaptureLocal([]portable.EncodedEnvelope{local}); err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{archiveObject(t, "observed", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, metrics := syncwork.WithWorkMetrics(ctx)
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
		t.Fatalf("observe: %v %v", ok, err)
	}
	counts := metrics.Counts()
	if counts["archive_audit_decodes"] != 1 || counts["archive_audit_verified_bytes_reused"] != 1 || counts["archive_audit_envelopes"] != 2 {
		t.Fatalf("audit coverage: %v", counts)
	}
	if _, err := store.DB.Exec(`DROP TRIGGER history_archive_envelope_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`UPDATE history_archive_envelopes SET envelope_json=? WHERE project_id=? AND envelope_hash=?`, []byte(`{}`), store.ProjectID, local.Envelope.EnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err == nil || ok {
		t.Fatal("complete observation hid corruption outside transport")
	}
	if allowed, all, err := archive.objects(ctx, "git:fixture", false); err == nil || allowed != nil || all != nil {
		t.Fatal("failed audit exposed a partial archive snapshot")
	}
}

func TestExactObservationSkipsRedundantEnvelopeMembershipAndOriginInserts(t *testing.T) {
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{archiveObject(t, "known", `{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	for _, table := range []string{"history_archive_envelopes", "history_archive_origins", "history_index_envelopes", "history_index_block_members"} {
		if _, err := store.DB.Exec(`CREATE TRIGGER fixture_noop_` + table + ` BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'redundant insert'); END`); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
		t.Fatalf("exact observation attempted redundant writes: %v %v", ok, err)
	}
	// A new source is a real provenance write, which must still be attempted.
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:new", ObservationStamp{}, true, []Block{block}); err == nil || ok {
		t.Fatal("new origin skipped its insert")
	}
}

func TestObservationExactByteReuseRejectsStoredIdentityAndProvenanceDamage(t *testing.T) {
	for _, field := range []string{"envelope_json", "record_type", "record_id", "record_revision", "initial_origin_hash"} {
		t.Run(field, func(t *testing.T) {
			ctx, store := archiveStoreFixture(t)
			activateEmptyArchive(t, ctx, store)
			archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
			index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
			object := archiveObject(t, "stored", `{}`)
			block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block}); err != nil || !ok {
				t.Fatalf("seed: %v %v", ok, err)
			}
			if _, err := store.DB.Exec(`DROP TRIGGER history_archive_envelope_immutable_update`); err != nil {
				t.Fatal(err)
			}
			value := any("damaged")
			switch field {
			case "envelope_json":
				value = append(append([]byte(nil), object.Bytes...), '\n')
			case "record_revision":
				value = 2
			case "initial_origin_hash":
				value = Hash("missing-origin")
			}
			conn, err := store.DB.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Model offline damage that bypassed the durable schema guards.
			if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			_, damageErr := conn.ExecContext(ctx, `UPDATE history_archive_envelopes SET `+field+`=? WHERE project_id=? AND envelope_hash=?`, value, store.ProjectID, object.Envelope.EnvelopeSHA256)
			_, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			conn.Close()
			if damageErr != nil {
				t.Fatal(damageErr)
			}
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			// The duplicate path must reject damage before adding new provenance.
			if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:new", ObservationStamp{}, true, []Block{block}); err == nil || ok {
				t.Fatal("damaged durable observation accepted")
			}
			var count int
			if err := store.DB.QueryRow(`SELECT count(*) FROM history_archive_origins WHERE project_id=? AND origin_hash=?`, store.ProjectID, Hash("git:new")).Scan(&count); err != nil || count != 0 {
				t.Fatalf("provenance leaked on failure: %d %v", count, err)
			}
			// Independently exercise audit reuse, rather than letting insertion's
			// duplicate check mask a missing audit identity/body/provenance check.
			err = store.view(ctx, func(tx *sql.Tx) error {
				_, err := store.auditVerifiedWithin(ctx, tx, map[string]portable.EncodedEnvelope{object.Envelope.EnvelopeSHA256: object})
				return err
			})
			if err == nil {
				t.Fatal("audit reused a damaged durable observation")
			}
		})
	}
}

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
	observed, accepted, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	stamp := observed.After
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
	if next, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", stamp, false, nil); err != nil || !ok || next.After != stamp {
		t.Fatalf("reuse: %+v %v %v", next, ok, err)
	}
	if _, err := store.DB.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, store.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", stamp, false, nil); err != nil || ok {
		t.Fatalf("stale observation accepted: %v %v", ok, err)
	}
	repaired, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil || !ok || repaired.After == stamp {
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
	if err != nil || !ok || after.After != (ObservationStamp{}) {
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
	initial, _, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, []Block{block})
	if err != nil {
		t.Fatal(err)
	}
	before := initial.After
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
