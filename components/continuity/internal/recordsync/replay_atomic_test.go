package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func atomicTopic(t *testing.T, title string) portable.EncodedEnvelope {
	t.Helper()
	ctx, db, project := indexedTestProject(t, "atomic-topic-"+title)
	if _, err := (topic.Service{DB: db}).Record(ctx, topic.Topic{ProjectID: project, ID: "fork", Title: title, Creator: "test"}); err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range exported.Envelopes {
		if object.Envelope.RecordType == "topic_revision" {
			return object
		}
	}
	t.Fatal("topic not exported")
	return portable.EncodedEnvelope{}
}
func atomicCount(t *testing.T, db *sql.DB, project, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func atomicPut(t *testing.T, f *replayFixture, objects ...portable.EncodedEnvelope) {
	t.Helper()
	for _, object := range objects {
		if _, err := f.archive.Put(object, f.scope.Endpoint); err != nil {
			t.Fatal(err)
		}
	}
}

func atomicRefresh(t *testing.T, f *replayFixture) {
	t.Helper()
	observed, err := f.index.ObservationStamp(f.ctx, f.archive)
	if err != nil {
		t.Fatal(err)
	}
	f.scope.Observed = observed
}

func TestAtomicSemanticImportRollsBackForksAndMainOnFailure(t *testing.T) {
	for _, mode := range []string{"statement", "commit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplayFixture(t)
			original := atomicTopic(t, "Original")
			atomicPut(t, &f, original)
			atomicRefresh(t, &f)
			f.stage(t)
			if result, err := f.replay(t); err != nil || result.ImportedRecords != 1 {
				t.Fatalf("baseline %+v %v", result, err)
			}
			variant := atomicTopic(t, "Variant")
			atomicPut(t, &f, variant)
			f.arrive(t, "reject")
			statements := []string{}
			if mode == "statement" {
				statements = append(statements, `CREATE TRIGGER atomic_reject BEFORE INSERT ON decision_records WHEN new.id='reject' BEGIN SELECT RAISE(ABORT,'fixture refuses main'); END`)
			}
			if mode == "commit" {
				statements = append(statements, `CREATE TABLE atomic_parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE atomic_child(id INTEGER REFERENCES atomic_parent(id) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER atomic_reject AFTER INSERT ON decision_records WHEN new.id='reject' BEGIN INSERT INTO atomic_child VALUES(1); END`)
			}
			for _, statement := range statements {
				if _, err := f.store.DB.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			atomicRefresh(t, &f)
			f.stage(t)
			receipts := atomicCount(t, f.store.DB, f.store.ProjectID, "portable_import_receipts")
			archive := atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes")
			f.store.DB.SetMaxOpenConns(1) // A failed COMMIT must leave its native connection reusable.
			if mode == "cancel" {
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				f.ctx = ctx
				calls := 0
				f.index.Now = func() time.Time {
					calls++
					if calls == 2 {
						cancel()
					}
					return time.Now().UTC()
				}
			}
			result, err := f.replay(t)
			if !errors.Is(err, ErrSemanticPending) || result.ImportedRecords != 0 || result.Forks != 0 || result.SupersessionForks != 0 {
				t.Fatalf("partial atomic result %+v %v", result, err)
			}
			for _, table := range []string{"fork_heads", "fork_head_observations", "decision_records"} {
				if n := atomicCount(t, f.store.DB, f.store.ProjectID, table); n != 0 {
					t.Fatalf("%s leaked %d staged rows", table, n)
				}
			}
			if atomicCount(t, f.store.DB, f.store.ProjectID, "portable_import_receipts") != receipts || atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes") != archive {
				t.Fatal("semantic rollback changed receipts/received archive")
			}
			// The original local revision remains; retries still have all transport input.
			if atomicCount(t, f.store.DB, f.store.ProjectID, "topic_revisions") != 1 {
				t.Fatal("rollback damaged existing revision")
			}
		})
	}
}

func TestAtomicSemanticCollisionDoesNotCommitOtherRecords(t *testing.T) {
	f := newReplayFixture(t)
	original := atomicTopic(t, "Original")
	atomicPut(t, &f, original)
	first := f.arrive(t, "identity")
	atomicRefresh(t, &f)
	f.stage(t)
	if _, err := f.replay(t); err != nil {
		t.Fatal(err)
	}
	variant := atomicTopic(t, "Variant")
	atomicPut(t, &f, variant)
	spec, err := portable.DecodeMemoryEnvelope(first.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(string(spec.Payload), "Observation", "Different observation"))
	collision, err := portable.EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	atomicPut(t, &f, collision)
	f.arrive(t, "otherwise-valid")
	atomicRefresh(t, &f)
	f.stage(t)
	result, err := f.replay(t)
	if err != nil || result.Collisions != 1 || result.ImportedRecords != 0 || result.Forks != 0 {
		t.Fatalf("collision not atomic %+v %v", result, err)
	}
	if atomicCount(t, f.store.DB, f.store.ProjectID, "decision_records") != 1 || atomicCount(t, f.store.DB, f.store.ProjectID, "fork_heads") != 0 || atomicCount(t, f.store.DB, f.store.ProjectID, "fork_head_observations") != 0 {
		t.Fatal("collision committed unrelated semantic progress")
	}
}

func TestAtomicSemanticFailureRollsBackRecoveryButPreservesTransport(t *testing.T) {
	f := newReplayFixture(t)
	const peerRoot = "/home/fixture/peer"
	if _, err := (worknarrative.Service{DB: f.source}).Append(f.ctx, worknarrative.Record{ID: "recovery-target", ProjectID: f.sourceProject, Title: "Recover original", Trigger: "manual", Creator: "test", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Read " + peerRoot, Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(f.ctx, f.source, f.sourceProject)
	if err != nil {
		t.Fatal(err)
	}
	var original portable.EncodedEnvelope
	for _, object := range exported.Envelopes {
		if object.Envelope.RecordType == "work_narrative" {
			original = object
		}
	}
	spec, err := portable.DecodeMemoryEnvelope(original.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	spec.Payload = []byte(strings.ReplaceAll(string(spec.Payload), peerRoot, "[PROJECT_ROOT]"))
	derived, err := portable.EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	correction, err := portable.RedactionRecoverySpec(portable.RedactionRecovery{Original: original.Bytes, Derived: derived.Bytes, ProjectRoot: peerRoot, Creator: "test", Reason: "Fixture transformation"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := portable.EncodeEnvelope(correction)
	if err != nil {
		t.Fatal(err)
	}
	atomicPut(t, &f, original, derived, recovery)
	f.arrive(t, "reject")
	if _, err := f.store.DB.Exec(`CREATE TRIGGER atomic_recovery_reject BEFORE INSERT ON decision_records WHEN new.id='reject' BEGIN SELECT RAISE(ABORT,'fixture rejects after recovery'); END`); err != nil {
		t.Fatal(err)
	}
	atomicRefresh(t, &f)
	f.stage(t)
	before := atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes")
	result, err := f.replay(t)
	if !errors.Is(err, ErrSemanticPending) || result.ImportedRecords != 0 {
		t.Fatalf("recovery import did not roll back %+v %v", result, err)
	}
	for _, table := range []string{"redaction_recoveries", "portable_envelopes", "portable_import_receipts", "work_narratives", "decision_records"} {
		if n := atomicCount(t, f.store.DB, f.store.ProjectID, table); n != 0 {
			t.Fatalf("%s leaked %d rows", table, n)
		}
	}
	if atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes") != before {
		t.Fatal("received transport lost")
	}
	if _, err := f.store.DB.Exec(`DROP TRIGGER atomic_recovery_reject`); err != nil {
		t.Fatal(err)
	}
	atomicRefresh(t, &f)
	result, err = f.replay(t)
	if err != nil || result.ImportedRecords != 3 || result.Collisions != 0 {
		t.Fatalf("atomic retry failed %+v %v", result, err)
	}
}

func TestAtomicBaselineTriggerCannotBlessCanonicalMutation(t *testing.T) {
	f := newReplayFixture(t)
	ctx, metrics := syncwork.WithWorkMetrics(f.ctx)
	f.ctx = ctx
	f.arrive(t, "first")
	atomicRefresh(t, &f)
	f.stage(t)
	if _, err := f.store.DB.Exec(`CREATE TRIGGER atomic_baseline_sideeffect AFTER INSERT ON history_archive_status WHEN new.consumer='` + Hash(replayJournalKey(f.scope.Consumer)) + `' BEGIN INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,recorded_at,schema_version) VALUES('unexpected',new.project_id,'fixture','Unexpected','Observation','accepted','owner','human','caller_asserted','','2026-01-01T00:00:00Z',1); END`); err != nil {
		t.Fatal(err)
	}
	result, err := f.replay(t)
	if err != nil || result.ImportedRecords != 1 {
		t.Fatalf("optional baseline failure lost semantic success %+v %v", result, err)
	}
	var unexpected, baseline int
	if err := f.store.DB.QueryRow(`SELECT COUNT(*) FROM decision_records WHERE project_id=? AND id='unexpected'`, f.store.ProjectID).Scan(&unexpected); err != nil {
		t.Fatal(err)
	}
	if err := f.store.DB.QueryRow(`SELECT COUNT(*) FROM history_archive_status WHERE project_id=? AND consumer=?`, f.store.ProjectID, Hash(replayJournalKey(f.scope.Consumer))).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	if metrics.Counts()["atomic_semantic_seed_sideeffects"] != 1 {
		t.Fatalf("side-effect guard not exercised: %v", metrics.Counts())
	}
	if unexpected != 0 || baseline != 0 {
		t.Fatalf("ineligible baseline/trigger side effect committed: %d %d", unexpected, baseline)
	}
}

// A closed database retains the public pending error identity.
func TestAtomicImporterClosedDatabaseKeepsPendingError(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "atomic-closed")
	db.Close()
	_, err := (Importer{DB: db, ProjectID: project}).Import(ctx, nil)
	if !errors.Is(err, ErrSemanticPending) {
		t.Fatalf("pending identity lost: %v", err)
	}
}

func TestAtomicSemanticImportRollsBackFailuresAfterMainApply(t *testing.T) {
	for _, mode := range []string{"adopt", "cancel_after_main", "baseline"} {
		t.Run(mode, func(t *testing.T) {
			f := newReplayFixture(t)
			if mode != "baseline" {
				atomicPut(t, &f, atomicTopic(t, "Original"), atomicTopic(t, "Variant"))
			}
			f.arrive(t, "main-record")
			var statement string
			if mode == "adopt" {
				statement = `CREATE TRIGGER atomic_adoption_reject BEFORE INSERT ON fork_heads BEGIN SELECT RAISE(ABORT,'fixture rejects adoption'); END`
			}
			if mode == "baseline" {
				statement = `CREATE TRIGGER atomic_baseline_reject BEFORE INSERT ON history_archive_status WHEN new.consumer='` + Hash(replayJournalKey(f.scope.Consumer)) + `' BEGIN SELECT RAISE(ABORT,'fixture rejects baseline'); END`
			}
			if statement != "" {
				if _, err := f.store.DB.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			atomicRefresh(t, &f)
			f.stage(t)
			received := atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes")
			f.store.DB.SetMaxOpenConns(1)
			if mode == "cancel_after_main" {
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				f.ctx = ctx
				calls := 0
				f.index.Now = func() time.Time {
					calls++
					if calls == 3 {
						cancel()
					}
					return time.Now().UTC()
				}
			}
			result, err := f.replay(t)
			if !errors.Is(err, ErrSemanticPending) || result.ImportedRecords != 0 || result.Forks != 0 {
				t.Fatalf("post-apply failure leaked result %+v %v", result, err)
			}
			for _, table := range []string{"decision_records", "topic_revisions", "fork_heads", "fork_head_observations", "portable_import_receipts", "portable_envelopes"} {
				if count := atomicCount(t, f.store.DB, f.store.ProjectID, table); count != 0 {
					t.Fatalf("%s retained %d rows", table, count)
				}
			}
			if atomicCount(t, f.store.DB, f.store.ProjectID, "history_archive_envelopes") != received {
				t.Fatal("post-apply rollback lost received transport")
			}
		})
	}
}
