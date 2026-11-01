package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

type replayFixture struct {
	publicationJournalFixture
	source        *sql.DB
	sourceProject string
}

func newReplayFixture(t *testing.T) replayFixture {
	t.Helper()
	_, source, project := indexedTestProject(t, "replay-source")
	return replayFixture{publicationJournalFixture: journalFixture(t), source: source, sourceProject: project}
}

func (f *replayFixture) arrive(t *testing.T, id string) portable.EncodedEnvelope {
	t.Helper()
	if _, err := (decision.Service{DB: f.source}).Append(f.ctx, decision.Record{ProjectID: f.sourceProject, ID: id, Title: id, Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	export, err := portable.EncodeMemoryExport(f.ctx, f.source, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range export.Envelopes {
		if object.Envelope.RecordType == "decision" && object.Envelope.RecordID == id {
			if _, err := f.archive.Put(object, f.scope.Endpoint); err != nil {
				t.Fatal(err)
			}
			return object
		}
	}
	t.Fatal("source decision absent")
	return portable.EncodedEnvelope{}
}

func (f *replayFixture) stage(t *testing.T) {
	t.Helper()
	plan := f.plan(t, false)
	if len(plan.Blocks) != 0 {
		f.commit(t, plan.Blocks)
	}
}

func (f *replayFixture) captureSource(t *testing.T) []portable.EncodedEnvelope {
	t.Helper()
	export, err := portable.EncodeMemoryExport(context.Background(), f.source, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range export.Envelopes {
		if _, err := f.archive.Put(object, f.scope.Endpoint); err != nil {
			t.Fatal(err)
		}
	}
	return export.Envelopes
}

func (f *replayFixture) replay(t *testing.T) (ImportResult, error) {
	t.Helper()
	return f.index.ImportArchiveJournal(f.ctx, f.archive, f.scope, func(context.Context) (JournalPublicationPlan, error) { return f.plan(t, true), nil })
}

func (f *replayFixture) state(t *testing.T) string {
	t.Helper()
	var state string
	if err := f.store.DB.QueryRow(`SELECT value_json FROM history_archive_status WHERE project_id=? AND consumer=?`, f.store.ProjectID, Hash(replayJournalKey(f.scope.Consumer))).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSemanticJournalImportsOnlyNewRecordsAndRepairsDamagedCache(t *testing.T) {
	f := newReplayFixture(t)
	first := f.arrive(t, "first")
	f.stage(t)
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("baseline: %+v %v", result, err)
	}
	f.arrive(t, "second")
	f.stage(t)
	ctx, metrics := syncwork.WithWorkMetrics(f.ctx)
	f.ctx = ctx
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("delta: %+v %v", result, err)
	}
	counts := metrics.Counts()
	if counts["semantic_journal_delta_cycles"] != 1 || counts["fallback_semantic_journal_full"] != 0 || counts["semantic_catalog_blocks_read"] != 0 || counts["dependency_membership_records"] != 1 || counts["archive_envelopes_read"] != 1 || counts["operational_full_loads"] != 0 || counts["local_state_records_visited"] != 0 {
		t.Fatalf("delta replay visited old state: %v", counts)
	}
	if _, err := f.store.DB.Exec(`UPDATE history_index_dependency_records SET selectors_sha256='damaged' WHERE project_id=? AND envelope_hash=?`, f.store.ProjectID, first.Envelope.EnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	f.arrive(t, "after-damage")
	f.stage(t)
	f.ctx, metrics = syncwork.WithWorkMetrics(context.Background())
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["fallback_semantic_journal_full"] != 1 {
		t.Fatalf("cache damage did not require complete replay: %+v %v %v", result, err, metrics.Counts())
	}
	f.arrive(t, "after-repair")
	f.stage(t)
	f.ctx, metrics = syncwork.WithWorkMetrics(context.Background())
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["semantic_journal_delta_cycles"] != 1 || metrics.Counts()["operational_full_loads"] != 0 {
		t.Fatalf("repaired cache failed to resume delta: %+v %v %v", result, err, metrics.Counts())
	}
	var localOrigins int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM history_archive_origins WHERE project_id=? AND origin_hash=?`, f.store.ProjectID, Hash("local")).Scan(&localOrigins); err != nil || localOrigins != 0 {
		t.Fatalf("semantic import invented local provenance: %d %v", localOrigins, err)
	}
}

func TestSemanticJournalRollbackKeepsRecordsCacheAndCursorsAtomic(t *testing.T) {
	for _, mode := range []string{"statement", "commit"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplayFixture(t)
			statements := []string{`CREATE TABLE fixture_replay_fault(enabled INTEGER NOT NULL)`, `INSERT INTO fixture_replay_fault VALUES(1)`}
			if mode == "commit" {
				statements = append(statements,
					`CREATE TABLE fixture_replay_parent(id INTEGER PRIMARY KEY)`,
					`CREATE TABLE fixture_replay_child(parent INTEGER REFERENCES fixture_replay_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
					`CREATE TRIGGER reject_semantic_replay AFTER INSERT ON decision_records WHEN new.id='reject-me' AND (SELECT enabled FROM fixture_replay_fault)=1 BEGIN INSERT INTO fixture_replay_child VALUES(1); END`)
			} else {
				statements = append(statements, `CREATE TRIGGER reject_semantic_replay BEFORE INSERT ON decision_records WHEN new.id='reject-me' AND (SELECT enabled FROM fixture_replay_fault)=1 BEGIN SELECT RAISE(ABORT,'fixture rejected semantic replay'); END`)
			}
			for _, statement := range statements {
				if _, err := f.store.DB.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			f.scope.Observed, err = f.index.ObservationStamp(f.ctx, f.archive)
			if err != nil {
				t.Fatal(err)
			}
			f.arrive(t, "baseline")
			f.stage(t)
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
				t.Fatalf("baseline: %+v %v", result, err)
			}
			before := f.state(t)
			rejected := f.arrive(t, "reject-me")
			f.stage(t)
			if result, err := f.replay(t); err == nil || result.ImportedRecords != 0 {
				t.Fatalf("rejected transaction reported committed records: %+v %v", result, err)
			}
			if f.state(t) != before {
				t.Fatal("failed transaction advanced semantic state")
			}
			var canonical, selectors int
			if err := f.store.DB.QueryRow(`SELECT count(*) FROM decision_records WHERE project_id=? AND id='reject-me'`, f.store.ProjectID).Scan(&canonical); err != nil || canonical != 0 {
				t.Fatalf("canonical rollback: %d %v", canonical, err)
			}
			if err := f.store.DB.QueryRow(`SELECT count(*) FROM history_index_dependency_records WHERE project_id=? AND envelope_hash=?`, f.store.ProjectID, rejected.Envelope.EnvelopeSHA256).Scan(&selectors); err != nil || selectors != 0 {
				t.Fatalf("selector rollback: %d %v", selectors, err)
			}
			if _, err := f.store.DB.Exec(`UPDATE fixture_replay_fault SET enabled=0`); err != nil {
				t.Fatal(err)
			}
			var metrics *syncwork.WorkMetrics
			f.ctx, metrics = syncwork.WithWorkMetrics(context.Background())
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["semantic_journal_delta_cycles"] != 1 {
				t.Fatalf("retry lost unacknowledged arrival: %+v %v %v", result, err, metrics.Counts())
			}
		})
	}
}

func TestSemanticJournalRejectsChangedCompleteSnapshotBeforeImport(t *testing.T) {
	f := newReplayFixture(t)
	f.arrive(t, "snapshot")
	f.stage(t)
	_, err := f.index.ImportArchiveJournal(f.ctx, f.archive, f.scope, func(context.Context) (JournalPublicationPlan, error) {
		plan := f.plan(t, true)
		plan.Snapshot = nil
		return plan, nil
	})
	if !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("changed snapshot accepted: %v", err)
	}
	var records int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM decision_records WHERE project_id=?`, f.store.ProjectID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("changed snapshot wrote canonical records: %d %v", records, err)
	}
}

func TestSemanticJournalDoesNotSeedAcrossConcurrentArchiveArrival(t *testing.T) {
	f := newReplayFixture(t)
	f.arrive(t, "snapshot")
	f.stage(t)
	result, err := f.index.ImportArchiveJournal(f.ctx, f.archive, f.scope, func(context.Context) (JournalPublicationPlan, error) {
		plan := f.plan(t, true)
		// Another writer commits after materialization but before semantic import.
		f.arrive(t, "concurrent")
		return plan, nil
	})
	if err != nil || result.ImportedRecords != 1 {
		t.Fatalf("snapshot import: %+v %v", result, err)
	}
	var receipts int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM history_archive_status WHERE project_id=? AND consumer=?`, f.store.ProjectID, Hash(replayJournalKey(f.scope.Consumer))).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("concurrent arrival acknowledged: %d %v", receipts, err)
	}
	f.stage(t)
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("concurrent record lost: %+v %v", result, err)
	}
	f.arrive(t, "next")
	f.stage(t)
	f.ctx, _ = syncwork.WithWorkMetrics(context.Background())
	if result, err := f.index.tryReplayJournal(f.ctx, f.archive, f.scope); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("stable baseline did not resume: %+v %v", result, err)
	}
}

func TestSemanticJournalOversizedArrivalRollsBackAndUsesReference(t *testing.T) {
	f := newReplayFixture(t)
	f.arrive(t, "baseline")
	f.stage(t)
	if _, err := f.replay(t); err != nil {
		t.Fatal(err)
	}
	before := f.state(t)
	for n := 0; n <= maxSemanticReplayRecords; n++ {
		id := fmt.Sprintf("arrival-%03d", n)
		if _, err := (decision.Service{DB: f.source}).Append(f.ctx, decision.Record{ProjectID: f.sourceProject, ID: id, Title: id, Rationale: "Observation", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	f.captureSource(t)
	f.stage(t)
	if result, err := f.index.tryReplayJournal(f.ctx, f.archive, f.scope); !errors.Is(err, errReplayJournalFull) || result.ImportedRecords != 0 || f.state(t) != before {
		t.Fatalf("oversized arrival advanced baseline: %+v %v", result, err)
	}
	var count int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM decision_records WHERE project_id=?`, f.store.ProjectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial oversized import survived: %d %v", count, err)
	}
	if result, err := f.replay(t); err != nil || result.ImportedRecords != maxSemanticReplayRecords+1 {
		t.Fatalf("reference lost oversized arrival: %+v %v", result, err)
	}
	f.arrive(t, "after-large-batch")
	f.stage(t)
	if result, err := f.index.tryReplayJournal(f.ctx, f.archive, f.scope); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("reference did not restore baseline: %+v %v", result, err)
	}
}

func TestSemanticJournalRejectsUnrelatedSelectorWritesDuringCanonicalApply(t *testing.T) {
	f := newReplayFixture(t)
	for _, statement := range []string{
		`CREATE TABLE fixture_selector_sideeffect(enabled INTEGER NOT NULL)`,
		`INSERT INTO fixture_selector_sideeffect VALUES(1)`,
		`CREATE TRIGGER disturb_old_selectors AFTER INSERT ON decision_records WHEN new.id='arrival' AND (SELECT enabled FROM fixture_selector_sideeffect)=1 BEGIN UPDATE history_index_dependency_records SET selectors_sha256='unrelated mutation' WHERE project_id=new.project_id AND envelope_hash<>(SELECT envelope_hash FROM history_archive_envelopes WHERE project_id=new.project_id AND record_id='arrival'); END`,
	} {
		if _, err := f.store.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	f.scope.Observed, err = f.index.ObservationStamp(f.ctx, f.archive)
	if err != nil {
		t.Fatal(err)
	}
	baseline := f.arrive(t, "baseline")
	f.stage(t)
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("baseline: %+v %v", result, err)
	}
	before := f.state(t)
	f.arrive(t, "arrival")
	f.stage(t)
	if result, err := f.index.tryReplayJournal(f.ctx, f.archive, f.scope); !errors.Is(err, errReplayJournalFull) || result.ImportedRecords != 0 {
		t.Fatalf("unrelated selector mutation was acknowledged: %+v %v", result, err)
	}
	if f.state(t) != before {
		t.Fatal("unrelated selector write advanced replay state")
	}
	var digest string
	if err := f.store.DB.QueryRow(`SELECT selectors_sha256 FROM history_index_dependency_records WHERE project_id=? AND envelope_hash=?`, f.store.ProjectID, baseline.Envelope.EnvelopeSHA256).Scan(&digest); err != nil || digest == "unrelated mutation" {
		t.Fatalf("unrelated cache write survived rollback: %s %v", digest, err)
	}
	if _, err := f.store.DB.Exec(`UPDATE fixture_selector_sideeffect SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	measured, metrics := syncwork.WithWorkMetrics(context.Background())
	f.ctx = measured
	if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["semantic_journal_delta_cycles"] != 1 {
		t.Fatalf("guarded retry failed: %+v %v %v", result, err, metrics.Counts())
	}
}

func TestSemanticJournalMissingDamagedAndCrossedReceiptsRequireReference(t *testing.T) {
	for _, mode := range []string{"state missing", "state checksum", "canonical missing", "canonical checksum", "archive missing", "archive checksum", "crossed canonical", "crossed archive"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplayFixture(t)
			f.arrive(t, "baseline")
			f.stage(t)
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
				t.Fatalf("baseline: %+v %v", result, err)
			}
			f.arrive(t, "arrival")
			f.stage(t)
			var err error
			switch mode {
			case "state missing":
				_, err = f.store.DB.Exec(`DELETE FROM history_archive_status WHERE project_id=? AND consumer=?`, f.store.ProjectID, Hash(replayJournalKey(f.scope.Consumer)))
			case "state checksum":
				_, err = f.store.DB.Exec(`UPDATE history_archive_status SET checksum='0000000000000000000000000000000000000000000000000000000000000000' WHERE project_id=? AND consumer=?`, f.store.ProjectID, Hash(replayJournalKey(f.scope.Consumer)))
			case "canonical missing":
				_, err = f.store.DB.Exec(`DELETE FROM history_sync_cursors WHERE project_id=? AND consumer=?`, f.store.ProjectID, replayCanonicalConsumer(f.scope.Consumer))
			case "canonical checksum":
				_, err = f.store.DB.Exec(`UPDATE history_sync_cursors SET checksum='0000000000000000000000000000000000000000000000000000000000000000' WHERE project_id=? AND consumer=?`, f.store.ProjectID, replayCanonicalConsumer(f.scope.Consumer))
			case "archive missing":
				_, err = f.store.DB.Exec(`DELETE FROM history_archive_cursors WHERE project_id=? AND consumer=?`, f.store.ProjectID, replayArchiveConsumer(f.scope.Consumer))
			case "archive checksum":
				_, err = f.store.DB.Exec(`UPDATE history_archive_cursors SET checksum='0000000000000000000000000000000000000000000000000000000000000000' WHERE project_id=? AND consumer=?`, f.store.ProjectID, replayArchiveConsumer(f.scope.Consumer))
			case "crossed canonical":
				digest := Hash(f.state(t))
				err = f.store.transact(f.ctx, func(tx *sql.Tx) error {
					cursor, err := database.SyncSnapshot(f.ctx, tx, f.store.ProjectID, replayCanonicalConsumer(f.scope.Consumer))
					if err != nil {
						return err
					}
					ok, err := database.RememberSyncCursorWithin(f.ctx, tx, cursor, digest)
					if err == nil && !ok {
						return errors.New("fixture cursor was not saved")
					}
					return err
				})
			case "crossed archive":
				err = f.store.transact(f.ctx, func(tx *sql.Tx) error {
					stamp, err := f.store.stampWithin(f.ctx, tx, "ready")
					if err != nil {
						return err
					}
					ok, err := f.store.rememberWithin(f.ctx, tx, replayArchiveConsumer(f.scope.Consumer), stamp)
					if err == nil && !ok {
						return errors.New("fixture archive cursor was not saved")
					}
					return err
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			measured, metrics := syncwork.WithWorkMetrics(context.Background())
			f.ctx = measured
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["fallback_semantic_journal_full"] != 1 {
				t.Fatalf("damaged/crossed receipt reused: %+v %v %v", result, err, metrics.Counts())
			}
			f.arrive(t, "after-repair")
			f.stage(t)
			measured, metrics = syncwork.WithWorkMetrics(context.Background())
			f.ctx = measured
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 || metrics.Counts()["semantic_journal_delta_cycles"] != 1 {
				t.Fatalf("receipt recovery failed: %+v %v %v", result, err, metrics.Counts())
			}
		})
	}
}
