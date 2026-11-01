package recordsync

import (
	"database/sql"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestArchiveObservationRetainsForeignOriginAcrossCanonicalCapture(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "foreign-capture")
	archive, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "observed", Title: "Observed", Rationale: "Retain origin", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, exported.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ObservePublication(ctx, archive, "git:source", block); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := archive.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	if reused, err := archive.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("capture not reusable: %v %v", reused, err)
	}
	for _, route := range []struct {
		endpoint string
		relay    bool
		want     int
	}{{"git:source", false, len(exported.Envelopes)}, {"folder:other", false, 0}, {"folder:other", true, len(exported.Envelopes)}} {
		objects, err := archive.Objects(ctx, route.endpoint, route.relay)
		if err != nil || len(objects) != route.want {
			t.Fatalf("canonical capture changed origin permission: %+v %d %v", route, len(objects), err)
		}
	}
}

func TestArchiveObservationCommitFailureRollsBackPublicationAndProofInputs(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "observation-commit")
	db.SetMaxOpenConns(1)
	archive, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	var before archiveStamp
	archiveTransaction(t, archive.store(), func(tx *sql.Tx) error {
		var err error
		before, err = archive.store().stampWithin(ctx, tx, "ready")
		return err
	})
	for _, query := range []string{
		`CREATE TABLE observation_commit_failure(project_id TEXT REFERENCES projects(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_observation_commit AFTER INSERT ON history_index_publications BEGIN INSERT INTO observation_commit_failure VALUES('absent-project'); END`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := index.ObservePublication(ctx, archive, "git:source", block); err == nil {
		t.Fatal("failed COMMIT reported success")
	}
	archiveTransaction(t, archive.store(), func(tx *sql.Tx) error {
		after, err := archive.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		if after != before {
			t.Fatalf("failed observation leaked body/origin/journal: %+v != %+v", after, before)
		}
		return nil
	})
	var memberships int
	if err := db.QueryRow(`SELECT count(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&memberships); err != nil || memberships != 0 {
		t.Fatalf("failed observation retained membership: %d %v", memberships, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_observation_commit`); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ObservePublication(ctx, archive, "git:source", block); err != nil {
		t.Fatalf("failed COMMIT poisoned connection: %v", err)
	}
	objects, err := archive.Objects(ctx, "git:source", false)
	if err != nil || len(objects) != 1 || string(objects[0].Bytes) != string(object.Bytes) {
		t.Fatalf("retry lost exact observation: %+v %v", objects, err)
	}
}

func TestArchiveObservationCanonicalSideEffectCannotReuseCapture(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "observation-canonical-change")
	archive, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := archive.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.AddBlock(ctx, block); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER observation_changes_canonical AFTER INSERT ON history_index_publications BEGIN UPDATE projects SET name='changed' WHERE id=new.project_id; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ObservePublication(ctx, archive, "git:source", block); err != nil {
		t.Fatal(err)
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	if reused, err := archive.CaptureProject(measured, db, project); err != nil || reused {
		t.Fatalf("canonical side effect hidden by observation: %v %v", reused, err)
	}
	if work.Counts()["capture_full_exports"] != 1 {
		t.Fatalf("unsupported canonical change skipped reference export: %v", work.Counts())
	}
}

func TestArchivePublicationRejectsOtherProjectBinding(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-binding")
	_, otherDB, otherProject := indexedTestProject(t, "publication-other")
	other, err := openTestArchive(t, otherDB, otherProject)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{fixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.ObservePublication(ctx, other, "git:source", block); err == nil {
		t.Fatal("cross-project observation accepted")
	}
	if _, err := index.PlanPublication(ctx, other, "git:source", true); err == nil {
		t.Fatal("cross-project publication accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM history_index_blocks WHERE project_id=?`, project).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected binding changed index: %d %v", count, err)
	}
}

func TestArchiveTrackingDamageInvalidatesCanonicalCapture(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "capture-tracking-damage")
	archive, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := archive.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	if reused, err := archive.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("initial proof unavailable: %v %v", reused, err)
	}
	if _, err := db.Exec(`UPDATE history_archive_state SET record_bytes=record_bytes+1 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if reused, err := archive.CaptureProject(ctx, db, project); err == nil || reused {
		t.Fatalf("damaged tracking retained capture proof: %v %v", reused, err)
	}
}
