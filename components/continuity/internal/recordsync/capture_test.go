package recordsync

import (
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestCaptureProjectReuseAndImmutableDamage(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "capture")
	a, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	warm := func() {
		t.Helper()
		for i := 0; i < 4; i++ {
			reused, err := a.CaptureProject(ctx, db, project)
			if err != nil {
				t.Fatal(err)
			}
			if reused {
				return
			}
		}
		t.Fatal("capture never reused")
	}
	measured, initialWork := syncwork.WithWorkMetrics(ctx)
	if reused, err := a.CaptureProject(measured, db, project); err != nil || reused {
		t.Fatalf("initial capture: %t %v", reused, err)
	}
	if counts := initialWork.Counts(); counts["fallback_capture_missing_cursor"] != 1 || counts["capture_full_exports"] != 1 || counts["export_writer_snapshot_reuses"] != 1 || counts["operational_full_loads"] != 1 || counts["fallback_export_writer_retry"] != 0 {
		t.Fatalf("missing initial fallback work: %v", counts)
	}
	warm()
	at := time.Now()
	for _, item := range []struct{ id, parent string }{{"root", ""}, {"child", "root"}} {
		if _, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: item.id, ProjectID: project, ParentInvocationID: item.parent, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("raw attribution invalidated capture: %t %v", reused, err)
	}
	if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ID: "work", ProjectID: project, Title: "Work", Trigger: "manual", Creator: "test", CreatorInvocationID: "child", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("new dependency skipped: %t %v", reused, err)
	}
	warm()
	export, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Envelopes) != 4 {
		t.Fatalf("missing attribution ancestors: %d", len(export.Envelopes))
	}
	hash := export.Envelopes[0].Envelope.EnvelopeSHA256
	if _, err := db.Exec(`UPDATE history_archive_envelopes SET envelope_json=cast('corrupt' as blob) WHERE project_id=? AND envelope_hash=?`, project, hash); err == nil {
		t.Fatal("immutable archive body changed")
	}
	warm()
	if _, err := db.Exec(`DROP TRIGGER history_archive_envelope_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE history_archive_envelopes SET envelope_json=cast('corrupt' as blob) WHERE project_id=? AND envelope_hash=?`, project, hash); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err == nil || reused {
		t.Fatalf("damaged immutable archive accepted: %v %v", reused, err)
	}

}

func TestCaptureProjectConsumesOnlyDerivedChanges(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "cursor")
	a, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err = a.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	// Index bookkeeping changes the generation but cannot change export bytes.
	if _, err = db.Exec(`INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,'metadata-only',1)`, project); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("metadata delta: %t %v", reused, err)
	}
	var pending int
	if err = db.QueryRow(`SELECT COUNT(*) FROM history_sync_changes c JOIN history_sync_cursors p ON c.project_id=p.project_id WHERE c.sequence>p.sequence AND c.project_id=?`, project).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("cursor failed to advance: %d %v", pending, err)
	}
	// A qualified canonical arrival publishes its delta and acknowledges both
	// that publication and its own retention writes without a second full export.
	if _, err = (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "second", Title: "Second", Rationale: "Arrival", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("canonical change: %t %v", reused, err)
	}
	reference, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range reference.Envelopes {
		var body []byte
		err := db.QueryRow(`SELECT envelope_json FROM history_archive_envelopes WHERE project_id=? AND envelope_hash=?`, project, object.Envelope.EnvelopeSHA256).Scan(&body)
		if err != nil || string(body) != string(object.Bytes) {
			t.Fatalf("reference differs: %v", err)
		}
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("canonical delta did not acknowledge retention/publication: %t %v", reused, err)
	}
	if _, err = db.Exec(`DELETE FROM history_sync_changes WHERE sequence=(SELECT MIN(sequence) FROM history_sync_changes)`); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("journal damage: %t %v", reused, err)
	}
	if _, err = db.Exec(`UPDATE history_sync_cursors SET sequence=sequence+1`); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("cursor damage: %t %v", reused, err)
	}
}

func TestCaptureProjectMissingCanonicalTriggerForcesCompleteExport(t *testing.T) {
	for _, mode := range []string{"missing", "modified", "restored"} {
		t.Run(mode, func(t *testing.T) { testCaptureProjectCanonicalTracking(t, mode) })
	}
}

func testCaptureProjectCanonicalTracking(t *testing.T, mode string) {
	ctx, db, project := indexedTestProject(t, "missing-canonical-trigger")
	a, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	appendDecision := func(id string) {
		t.Helper()
		if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: id, Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	appendDecision("before")
	for i := 0; i < 3; i++ {
		if _, err := a.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='history_sync_decision_records_insert'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER history_sync_decision_records_insert`); err != nil {
		t.Fatal(err)
	}
	if mode == "modified" {
		if _, err := db.Exec(`CREATE TRIGGER history_sync_decision_records_insert AFTER INSERT ON decision_records BEGIN SELECT 1; END`); err != nil {
			t.Fatal(err)
		}
	}
	appendDecision("untracked-arrival")
	if mode == "restored" {
		if _, err := db.Exec(definition); err != nil {
			t.Fatal(err)
		}
	}
	measured, work := syncwork.WithWorkMetrics(ctx)
	if reused, err := a.CaptureProject(measured, db, project); err != nil || reused || work.Counts()["capture_full_exports"] != 1 {
		t.Fatalf("missing canonical tracking reused capture: %t %v %v", reused, err, work.Counts())
	}
	var records int
	if err := db.QueryRow(`SELECT count(*) FROM history_archive_envelopes WHERE project_id=?`, project).Scan(&records); err != nil || records != 2 {
		t.Fatalf("untracked arrival lost: %d %v", records, err)
	}
}
