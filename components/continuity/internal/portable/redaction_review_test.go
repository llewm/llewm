package portable

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

// legacyRedactionCheckpoint is a checkpoint envelope that an earlier redaction
// policy admitted and current admission rejects. It is built directly, never by
// mutating a stored record.
func legacyRedactionCheckpoint(t *testing.T, lineage, projectID string, at time.Time) (EnvelopeSpec, EncodedEnvelope) {
	t.Helper()
	old := checkpoint.Checkpoint{ID: "legacy-checkpoint", ProjectID: projectID, Task: "Historical sync", Status: "completed", Summary: "https://historical-user@example.test/history", NextAction: "Complete", Authority: "caller_asserted", CreatedAt: at}
	spec, err := checkpointSpec(lineage, projectID, "/source", old)
	if err != nil {
		t.Fatal(err)
	}
	var payload checkpointPayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Summary = old.Summary
	if spec.Payload, err = json.Marshal(payload); err != nil {
		t.Fatal(err)
	}
	legacy, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{legacy}}); err == nil {
		t.Fatal("fixture does not exercise changed redaction rules")
	}
	return spec, legacy
}

func TestFreshPeerImportsReviewedRedactionProjection(t *testing.T) {
	for _, mode := range []string{"memory", "transaction", "bundle"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, db, id := operationalProject(t, "fresh-peer-redaction-"+mode)
			lineage := "lineage:cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
			if _, err := (Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
			spec, legacy := legacyRedactionCheckpoint(t, lineage, id, at)
			action := EnvelopeSpec{LineageID: lineage, RecordType: "checkpoint_action", RecordSchemaVersion: 1, RecordID: "legacy-action", RecordedAt: at, Authority: "caller_asserted", Index: json.RawMessage(`{"title":"Follow up"}`), Refs: []Reference{{Kind: "checkpoint", ID: spec.RecordID}}, Payload: json.RawMessage(`{"source_checkpoint_id":"legacy-checkpoint","description":"Follow up"}`)}
			specs := []EnvelopeSpec{spec, action}
			importAll := func() (ImportPlan, error) {
				switch mode {
				case "memory":
					plan, _, err := ImportMemoryRecords(ctx, db, id, specs, at)
					return plan, err
				case "transaction":
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					plan, _, err := ImportMemoryRecordsTx(ctx, tx, id, specs, at, RecoveryComparisonComplete)
					if err == nil && plan.ApplyAllowed {
						return plan, tx.Commit()
					}
					tx.Rollback()
					return plan, err
				default:
					built, err := memoryImportBundle(ctx, db, id, specs, at)
					if err != nil {
						t.Fatal(err)
					}
					result, err := ApplyOperationalImport(ctx, db, id, built.ManifestBytes, built.RecordsBytes, at)
					if err != nil && !result.Plan.ApplyAllowed {
						// An inapplicable plan is the refusal under test, not a failure.
						err = nil
					}
					return result.Plan, err
				}
			}

			// Without a review, a fresh peer refuses the whole batch and writes nothing.
			plan, err := importAll()
			if err != nil || plan.ApplyAllowed {
				t.Fatalf("unreviewed legacy record admitted: %+v %v", plan, err)
			}
			if tableCount(t, db, "checkpoints") != 0 || tableCount(t, db, "portable_envelopes") != 0 {
				t.Fatal("refused import wrote records")
			}

			if _, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "", "reason", at); err == nil {
				t.Fatal("review without a reviewer accepted")
			}
			review, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "engineer", "historical HTTPS username", at)
			if err != nil || review.SourceSHA256 != legacy.Envelope.EnvelopeSHA256 || review.RecordID != spec.RecordID {
				t.Fatalf("review: %+v %v", review, err)
			}
			if again, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "engineer", "historical HTTPS username", at); err != nil || again.ProjectedSHA256 != review.ProjectedSHA256 {
				t.Fatalf("repeated review: %+v %v", again, err)
			}

			plan, err = importAll()
			if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] != 2 {
				t.Fatalf("reviewed import: %+v %v", plan, err)
			}
			var payload string
			if err := db.QueryRowContext(ctx, `SELECT payload_json FROM checkpoints WHERE id=?`, spec.RecordID).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(payload, "historical-user") {
				t.Fatal("operational row holds the unredacted source")
			}
			if tableCount(t, db, "checkpoint_actions") != 1 {
				t.Fatal("dependent action not imported with its projected checkpoint")
			}
			var retained string
			if err := db.QueryRowContext(ctx, `SELECT envelope_json FROM portable_envelopes WHERE record_type='checkpoint' AND record_id=?`, spec.RecordID).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(legacy.Bytes, []byte(retained)) {
				t.Fatal("source envelope not retained exactly")
			}

			// This peer now presents the source bytes, like every existing peer,
			// so a repeated sync is an exact replay and writes nothing.
			built, err := memoryImportBundle(ctx, db, id, specs, at)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := PlanOperationalImport(ctx, db, id, built.ManifestBytes, built.RecordsBytes)
			if err != nil || !replay.ApplyAllowed || replay.Counts[PlanAlreadyPresent] != 2 || replay.Counts[PlanAppend] != 0 {
				t.Fatalf("repeated sync: %+v %v", replay, err)
			}
			for _, item := range replay.Items {
				if item.RecordType == "checkpoint" && item.ExistingEnvelopeSHA256 != legacy.Envelope.EnvelopeSHA256 {
					t.Fatalf("local snapshot exports projected bytes as the source identity: %+v", item)
				}
			}
			local, err := LocalEnvelopes(ctx, db, id)
			if err != nil {
				t.Fatal(err)
			}
			if got := local[RecordKey("checkpoint", spec.RecordID, 0)]; got.Envelope.EnvelopeSHA256 != legacy.Envelope.EnvelopeSHA256 {
				t.Fatalf("export would publish a new version of the source identity: %s", got.Envelope.EnvelopeSHA256)
			}
		})
	}
}

func TestRedactionReviewRefusesWithoutExactSourceProof(t *testing.T) {
	t.Parallel()
	ctx, db, id := operationalProject(t, "redaction-review-proof")
	lineage := "lineage:efefefefefefefefefefefefefefefef"
	if _, err := (Service{DB: db}).Initialize(ctx, id, lineage); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	spec, legacy := legacyRedactionCheckpoint(t, lineage, id, at)
	if _, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "engineer", "historical HTTPS username", at); err != nil {
		t.Fatal(err)
	}
	// A review names one projection. If its recorded hash no longer matches the
	// recomputed projection, import refuses instead of trusting the source hash.
	if _, err := db.ExecContext(ctx, `UPDATE redaction_projection_reviews SET projected_sha256=? WHERE project_id=?`, strings.Repeat("0", 64), id); err != nil {
		t.Fatal(err)
	}
	plan, _, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{spec}, at)
	if err != nil || plan.ApplyAllowed || tableCount(t, db, "checkpoints") != 0 {
		t.Fatalf("mismatched review admitted: %+v %v", plan, err)
	}
	if len(plan.Items) != 1 || strings.Contains(plan.Items[0].Reason, "historical-user") || strings.Contains(plan.Items[0].Reason, spec.RecordID) {
		t.Fatalf("refusal echoes source identity or content: %+v", plan.Items)
	}
	if _, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "engineer", "again", at); err == nil {
		t.Fatal("conflicting review replaced the held one")
	}
	// A different source under the same identity has no review and stays refused.
	changed := spec
	changed.Payload = bytes.Replace(spec.Payload, []byte("historical-user"), []byte("changed-user"), 1)
	if _, err := db.ExecContext(ctx, `DELETE FROM redaction_projection_reviews WHERE project_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewRedactionProjection(ctx, db, id, legacy.Bytes, "engineer", "historical HTTPS username", at); err != nil {
		t.Fatal(err)
	}
	refused, _, err := ImportMemoryRecords(ctx, db, id, []EnvelopeSpec{changed}, at)
	if err != nil || refused.ApplyAllowed {
		t.Fatalf("unreviewed source admitted through another source's review: %+v %v", refused, err)
	}
	// An already-admissible record cannot be reviewed.
	safe := checkpoint.Checkpoint{ID: "safe", ProjectID: id, Task: "Safe", Status: "completed", Summary: "Plain", NextAction: "None", Authority: "caller_asserted", CreatedAt: at}
	safeSpec, err := checkpointSpec(lineage, id, "/source", safe)
	if err != nil {
		t.Fatal(err)
	}
	safeEnvelope, err := EncodeEnvelope(safeSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewRedactionProjection(ctx, db, id, safeEnvelope.Bytes, "engineer", "not needed", at); err == nil {
		t.Fatal("review accepted for an admissible source")
	}
}
