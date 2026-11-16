package portable

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

func TestExactHistoricalReplayAfterRedactionUpgrade(t *testing.T) {
	for _, mode := range []string{"memory", "transaction", "bundle"} {
		t.Run(mode, func(t *testing.T) {
			ctx, db, id := operationalProject(t, "legacy-redaction-"+mode)
			lineage := "lineage:abababababababababababababababab"
			if _, err := (Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			old := checkpoint.Checkpoint{ID: "legacy-checkpoint", ProjectID: id, Task: "Historical sync", Status: "completed", Summary: "https://historical-user@example.test/history", NextAction: "Complete", Authority: "caller_asserted", CreatedAt: at}
			body, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			// Model a record accepted by the previous redaction policy. Never mutate
			// an existing operational record or retained observation to set up replay.
			if _, err := db.ExecContext(ctx, `INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, old.ID, id, old.Task, old.Status, old.Summary, old.NextAction, string(body), at.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			spec, err := checkpointSpec(lineage, id, "/source", old)
			if err != nil {
				t.Fatal(err)
			}
			var payload checkpointPayload
			if err := json.Unmarshal(spec.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload.Summary = old.Summary
			spec.Payload, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			legacy, err := EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{legacy}}); err == nil {
				t.Fatal("fixture does not exercise changed redaction rules")
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(?,?,?,?,?,'import')`, id, "checkpoint", old.ID, 0, string(legacy.Bytes)); err != nil {
				t.Fatal(err)
			}
			fresh := old
			fresh.ID = "new-checkpoint"
			fresh.Summary = "New safe record"
			next, err := checkpointSpec(lineage, id, "/source", fresh)
			if err != nil {
				t.Fatal(err)
			}
			action := EnvelopeSpec{LineageID: lineage, RecordType: "checkpoint_action", RecordSchemaVersion: 1, RecordID: "new-action", RecordedAt: at, Authority: "caller_asserted", Index: json.RawMessage(`{"title":"Follow up"}`), Refs: []Reference{{Kind: "checkpoint", ID: old.ID}}, Payload: json.RawMessage(`{"source_checkpoint_id":"legacy-checkpoint","description":"Follow up"}`)}
			specs := []EnvelopeSpec{spec, next, action}
			built, err := memoryImportBundle(ctx, db, id, specs, at)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PlanOperationalImport(ctx, db, id, built.ManifestBytes, built.RecordsBytes)
			if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAlreadyPresent] != 1 || plan.Counts[PlanAppend] != 2 {
				t.Fatalf("plan: %+v %v", plan, err)
			}
			switch mode {
			case "memory":
				plan, _, err = ImportMemoryRecords(ctx, db, id, specs, at)
			case "transaction":
				tx, e := db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				plan, _, err = ImportMemoryRecordsTx(ctx, tx, id, specs, at, RecoveryComparisonComplete)
				if err == nil && plan.ApplyAllowed {
					err = tx.Commit()
				} else {
					tx.Rollback()
				}
			case "bundle":
				result, e := ApplyOperationalImport(ctx, db, id, built.ManifestBytes, built.RecordsBytes, at)
				plan, err = result.Plan, e
			}
			if err != nil || !plan.ApplyAllowed {
				t.Fatalf("apply: %+v %v", plan, err)
			}
			var unchanged string
			if err := db.QueryRowContext(ctx, `SELECT payload_json FROM checkpoints WHERE id=?`, old.ID).Scan(&unchanged); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, []byte(unchanged)) {
				t.Fatal("historical payload rewritten")
			}
			if err := db.QueryRowContext(ctx, `SELECT envelope_json FROM portable_envelopes WHERE record_id=?`, old.ID).Scan(&unchanged); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(legacy.Bytes, []byte(unchanged)) {
				t.Fatal("retained bytes rewritten")
			}
			if tableCount(t, db, "checkpoints") != 2 {
				t.Fatal("new record not imported")
			}
			if tableCount(t, db, "checkpoint_actions") != 1 {
				t.Fatal("dependency on the omitted present checkpoint was lost")
			}
			changed := spec
			changed.Payload = bytes.Replace(spec.Payload, []byte("historical-user"), []byte("changed-user"), 1)
			refused, _, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{changed}, at)
			if err != nil || refused.ApplyAllowed {
				t.Fatalf("changed historical observation accepted: %+v %v", refused, err)
			}
			// The same payload under a new identity remains inadmissible. Equality
			// with an operationally present local observation is the required proof.
			spec.RecordID = "unknown-legacy"
			rejected, _, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{spec}, at)
			if err != nil || rejected.ApplyAllowed || rejected.Counts[PlanInvalid] != 1 {
				t.Fatalf("new historical payload accepted: %+v %v", rejected, err)
			}
			if tableCount(t, db, "checkpoints") != 2 {
				t.Fatal("rejected import wrote payload")
			}
			orphan, err := EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(?,?,?,?,?,'import')`, id, "checkpoint", spec.RecordID, 0, string(orphan.Bytes)); err != nil {
				t.Fatal(err)
			}
			rejected, _, err = ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{spec}, at)
			if err != nil || rejected.ApplyAllowed || rejected.Counts[PlanInvalid] != 1 {
				t.Fatalf("retention without an operational record authorized import: %+v %v", rejected, err)
			}
		})
	}
}
