package portable

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"github.com/llewm/llewm/components/continuity/internal/topic"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestMemoryDeltaMatchesFullExportAndConsumesRetention(t *testing.T) {
	ctx, db, project := operationalProject(t, "delta")
	if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	decisions := decision.Service{DB: db}
	appendDecision := func(id string) {
		t.Helper()
		_, err := decisions.Append(ctx, decision.Record{ProjectID: project, ID: id, Title: "Decision", Rationale: "token=fixture-secret", Disposition: "accepted", Actor: "owner", ActorType: "human", Authority: "caller_asserted", Links: []decision.Link{{Kind: "subject", TargetKind: "task", TargetID: "delta"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendDecision("baseline")
	baseline, err := EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	materialized := map[string][]byte{}
	for _, e := range baseline.Envelopes {
		materialized[recordKey(e.Envelope)] = e.Bytes
	}
	read := func() (database.SyncCursor, []database.SyncChange, string) {
		t.Helper()
		c, changes, reason, err := database.ReadSyncChanges(ctx, db, project, "export-test", "archive", 4096)
		if err != nil {
			t.Fatal(err)
		}
		return c, changes, reason
	}
	c, _, reason := read()
	if reason != "missing_cursor" {
		t.Fatal(reason)
	}
	if ok, err := database.RememberSyncCursor(ctx, db, c, "archive"); err != nil || !ok {
		t.Fatalf("baseline receipt: %t %v", ok, err)
	}
	for cycle := 0; cycle < 3; cycle++ {
		appendDecision(fmt.Sprintf("arrival-%d", cycle))
		at := time.Date(2020, 1, 1, 0, 0, cycle, 0, time.UTC) // late historical timestamps
		for _, item := range []struct{ id, parent string }{{fmt.Sprintf("parent-%d", cycle), ""}, {fmt.Sprintf("actor-%d", cycle), fmt.Sprintf("parent-%d", cycle)}} {
			if _, err := (attribution.Service{DB: db}).AppendInvocation(ctx, attribution.Invocation{ID: item.id, ProjectID: project, ParentInvocationID: item.parent, ActorType: "agent", Model: "fixture", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{ProjectID: project, ID: fmt.Sprintf("narrative-%d", cycle), Title: "Observation", Trigger: "manual", Creator: "test", CreatorInvocationID: fmt.Sprintf("actor-%d", cycle), Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := (topic.Service{DB: db}).Record(ctx, topic.Topic{ProjectID: project, ID: "delta-topic", Title: "Topic", Summary: "New revision", Status: "proposed", Creator: "test"}); err != nil {
			t.Fatal(err)
		}
		if cycle == 0 {
			if err := (topic.Service{DB: db}).Alias(ctx, project, "delta-topic", "alias", "test"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := (resource.Service{DB: db}).Record(ctx, resource.Resource{ProjectID: project, ID: "host", Kind: "host", Title: "Host", Summary: fmt.Sprintf("Observation%d", cycle), Creator: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := (resource.Service{DB: db}).AddNote(ctx, resource.Note{ProjectID: project, ResourceID: "host", Text: "Observation", Authority: "machine_observed", Creator: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := (procedures.Service{DB: db}).Append(ctx, procedures.Procedure{ProjectID: project, ID: "checks", Title: "Checks", Purpose: "Verify", Status: "accepted", Platforms: []string{"linux"}, Steps: []procedures.Step{{ID: "test", Description: "Test", StageKind: "source_check", StageID: "go-test"}}, Source: "fixture"}); err != nil {
			t.Fatal(err)
		}
		if _, err := (workitem.Service{DB: db}).Capture(ctx, workitem.Capture{Item: workitem.Item{ProjectID: project, ID: fmt.Sprintf("item-%d", cycle), Kind: "task", Title: "Task", Body: "Observation", Creator: "test", Tags: []string{"delta"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := (trackplan.Service{DB: db}).AppendPlan(ctx, trackplan.Plan{ProjectID: project, ID: "delta-plan", RoadmapNodeID: "delta", Title: "Plan", Summary: "Delta", Status: "active", Creator: "test", Items: []trackplan.Item{{ID: "one", Title: "First", Position: 1, Status: "active"}, {ID: "two", Title: "Second", Position: 2, Status: "pending", DependsOn: []string{"one"}}}}); err != nil {
			t.Fatal(err)
		}
		c, changes, reason := read()
		if reason != "" {
			t.Fatal(reason)
		}
		measured, deltaWork := syncwork.WithWorkMetrics(ctx)
		export, receipt, used, err := EncodeMemoryDelta(measured, db, c, changes)
		if err != nil || !used {
			t.Fatalf("delta cycle%d: used=%t %v changes=%+v", cycle, used, err, changes)
		}
		if receipt.Sequence <= c.Sequence || receipt.Generation == c.Generation {
			t.Fatal("retention writes absent from receipt")
		}
		for _, e := range export.Envelopes {
			if _, old := materialized[recordKey(e.Envelope)]; old {
				t.Fatalf("replayed unrelated history: %s", recordKey(e.Envelope))
			}
			if bytes.Contains(e.Bytes, []byte("fixture-secret")) {
				t.Fatal("delta bypassed redaction")
			}
			materialized[recordKey(e.Envelope)] = e.Bytes
		}
		measured, fullWork := syncwork.WithWorkMetrics(ctx)
		full, err := EncodeMemoryExport(measured, db, project)
		if err != nil {
			t.Fatal(err)
		}
		deltaCounts, fullCounts := deltaWork.Counts(), fullWork.Counts()
		if deltaCounts["operational_delta_loads"] != 1 || deltaCounts["operational_full_loads"] != 0 || deltaCounts["operational_specs_loaded"] != int64(len(export.Envelopes)) || deltaCounts["snapshot_envelope_encodes"] == 0 {
			t.Fatalf("missing selected export work: %v", deltaCounts)
		}
		if fullCounts["operational_full_loads"] != 1 || fullCounts["operational_specs_loaded"] != int64(len(full.Envelopes)) || fullCounts["snapshot_encoding_reuses"] != int64(len(full.Envelopes)) || fullCounts["retained_envelopes_decoded"] != int64(len(full.Envelopes)) || fullCounts["retained_envelope_bytes_decoded"] == 0 {
			t.Fatalf("missing complete snapshot work: %v", fullCounts)
		}
		if len(full.Envelopes) != len(materialized) {
			t.Fatalf("missing delta records: full=%d materialized=%d", len(full.Envelopes), len(materialized))
		}
		for _, e := range full.Envelopes {
			if !bytes.Equal(e.Bytes, materialized[recordKey(e.Envelope)]) {
				t.Fatalf("reference mismatch: %s", recordKey(e.Envelope))
			}
		}
		if ok, err := database.RememberSyncCursor(ctx, db, receipt, "archive"); err != nil || !ok {
			t.Fatalf("receipt: %t %v", ok, err)
		}
		_, pending, reason := read()
		if reason != "" || len(pending) != 0 {
			t.Fatalf("own retention replayed: %s %d", reason, len(pending))
		}
	}
}

func TestMemoryDeltaRefusesStaleUnsupportedAndOversizeBatches(t *testing.T) {
	ctx, db, project := operationalProject(t, "delta-fallback")
	if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	c, _, _, err := database.ReadSyncChanges(ctx, db, project, "test", "archive", 10)
	if err != nil {
		t.Fatal(err)
	}
	change := database.SyncChange{Table: "decision_records", Key: fmt.Sprintf(`[%q,"arrival"]`, project), Operation: "insert"}
	measured, work := syncwork.WithWorkMetrics(ctx)
	for _, changes := range [][]database.SyncChange{
		{{Table: "portable_envelopes", Key: `[]`, Operation: "insert"}},
		{{Table: "decision_records", Key: change.Key, Operation: "delete"}},
		{{Table: "decision_records", Key: change.Key, Operation: "update_old"}},
		{{Table: "decision_records", Key: `["other-project","arrival"]`, Operation: "insert"}},
		{{Table: "decision_records", Key: `[]`, Operation: "insert"}},
	} {
		if _, _, used, err := EncodeMemoryDelta(measured, db, c, changes); err != nil || used {
			t.Fatalf("unsupported batch accepted: %t %v", used, err)
		}
	}
	var large []database.SyncChange
	for i := 0; i < 129; i++ {
		large = append(large, database.SyncChange{Table: "decision_records", Key: fmt.Sprintf(`[%q,"record-%d"]`, project, i), Operation: "insert"})
	}
	if _, _, used, err := EncodeMemoryDelta(measured, db, c, large); err != nil || used {
		t.Fatalf("oversize accepted: %t %v", used, err)
	}
	if _, err := db.Exec(`INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,'racing',1)`, project); err != nil {
		t.Fatal(err)
	}
	if _, _, used, err := EncodeMemoryDelta(measured, db, c, []database.SyncChange{change}); err != nil || used {
		t.Fatalf("stale snapshot accepted: %t %v", used, err)
	}
	if counts := work.Counts(); counts["fallback_delta_unqualified_changes"] != 6 || counts["fallback_delta_snapshot_changed"] != 1 || counts["operational_specs_loaded"] != 0 {
		t.Fatalf("fallback work misreported: %v", counts)
	}
}

func TestExportDeltaSelectionUsesBoundIDs(t *testing.T) {
	ctx, db, project := operationalProject(t, "selected-query")
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "unselected", Title: "Unselected", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	// A bound ID containing SQL punctuation must never widen the selection.
	source := &deltaSource{queryer: db, keys: map[string][][]any{"decision_records": {{"x' OR 1=1 --"}}}}
	rows, err := exportRows(ctx, source, "decision_records", `SELECT id FROM decision_records WHERE project_id=? ORDER BY id`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("selection widened")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryDeltaRollsBackRetentionBeforeReplay(t *testing.T) {
	ctx, db, project := operationalProject(t, "delta-rollback")
	if _, err := (Service{DB: db}).Initialize(ctx, project, "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	baseline, _, _, err := database.ReadSyncChanges(ctx, db, project, "rollback", "archive", 100)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := database.RememberSyncCursor(ctx, db, baseline, "archive"); err != nil || !ok {
		t.Fatalf("baseline: %t %v", ok, err)
	}
	for _, id := range []string{"first", "second"} {
		if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: id, Title: id, Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	c, changes, reason, err := database.ReadSyncChanges(ctx, db, project, "rollback", "archive", 100)
	if err != nil || reason != "" {
		t.Fatalf("changes: %s %v", reason, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_delta_retention BEFORE INSERT ON portable_envelopes WHEN new.record_id='second' BEGIN SELECT RAISE(ABORT,'fixture interrupted retention'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, used, err := EncodeMemoryDelta(ctx, db, c, changes); err == nil || used {
		t.Fatalf("interrupted retention succeeded: %t %v", used, err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM portable_envelopes WHERE project_id=?`, project).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("partial retention leaked: %d %v", retained, err)
	}
	replayed, pending, reason, err := database.ReadSyncChanges(ctx, db, project, "rollback", "archive", 100)
	if err != nil || reason != "" || replayed.Generation != c.Generation || replayed.Sequence != c.Sequence || len(pending) != len(changes) {
		t.Fatalf("rollback changed receipt: %+v %s %v", replayed, reason, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_delta_retention`); err != nil {
		t.Fatal(err)
	}
	if export, receipt, used, err := EncodeMemoryDelta(ctx, db, c, changes); err != nil || !used || len(export.Envelopes) != 2 || receipt.Sequence <= c.Sequence {
		t.Fatalf("replay failed: %+v %t %v", receipt, used, err)
	}
}
