package memsync

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestRedactionProjectionPreviewIsReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newMachine(t, "projection-preview", bareRemote(t))
	source, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: testLineage, RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: "historical", Authority: "caller_asserted", RecordedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), Index: json.RawMessage(`{"title":"Historical","status":"completed"}`), Payload: json.RawMessage(`{"task":"Historical","status":"completed","summary":"https://fixture-user:fixture-password@example.test/history","next_action":"Done","operational_schema_version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{source})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitBlocks(ctx, repo, testLineage, []recordsync.Block{block}, "fixture: historical observation", false); err != nil {
		t.Fatal(err)
	}
	before, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := m.syncer.PreviewRedactionProjection(ctx, source.Envelope.EnvelopeSHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.ReviewRequired || preview.Applied || preview.SourceSHA256 != source.Envelope.EnvelopeSHA256 {
		t.Fatalf("invalid preview: %+v", preview)
	}
	if bytes.Contains(result, []byte("fixture-password")) || bytes.Contains(result, []byte("fixture-user")) {
		t.Fatal("preview exposed original credentials")
	}
	after, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil || before != after {
		t.Fatal("preview changed Git history")
	}
	for _, table := range []string{"checkpoints", "portable_envelopes", "portable_import_receipts", "history_index_blocks", "history_index_envelopes"} {
		var count int
		if err := m.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("preview wrote %s: %d %v", table, count, err)
		}
	}
	held, err := m.syncer.trackedBlockObservations(ctx, repo, testLineage, source.Envelope.EnvelopeSHA256)
	if err != nil || !bytes.Equal(held[source.Envelope.EnvelopeSHA256], source.Bytes) {
		t.Fatal("source observation changed")
	}
	for _, hash := range []string{"bad", strings.Repeat("A", 64), strings.Repeat("0", 64)} {
		if _, err := m.syncer.PreviewRedactionProjection(ctx, hash); err == nil {
			t.Fatal("accepted invalid/missing source")
		}
	}
}

func TestFreshPeerSyncsAfterLocalRedactionReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := bareRemote(t)
	origin := newMachine(t, "projection-origin", remote)
	source, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: testLineage, RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: "historical", Authority: "caller_asserted", RecordedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), Index: json.RawMessage(`{"title":"Historical","status":"completed"}`), Payload: json.RawMessage(`{"task":"Historical","status":"completed","summary":"https://fixture-user:fixture-password@example.test/history","next_action":"Done","operational_schema_version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{source})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := origin.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Model history published under an earlier redaction policy.
	if _, err := commitBlocks(ctx, repo, testLineage, []recordsync.Block{block}, "fixture: historical observation", false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	fresh := newMachine(t, "projection-fresh", remote)
	if result, err := fresh.syncer.Sync(ctx); err == nil && result.Outcome == OutcomeSynced {
		t.Fatalf("fresh peer imported legacy history without review: %+v", result)
	}
	if count := tableRows(t, fresh, "checkpoints"); count != 0 {
		t.Fatalf("refused sync imported %d checkpoints", count)
	}

	if _, err := fresh.syncer.ReviewRedactionProjections(ctx, RedactionReviewRequest{All: true, Apply: true}); err == nil {
		t.Fatal("applied a review without reviewer and reason")
	}
	preview, err := fresh.syncer.ReviewRedactionProjections(ctx, RedactionReviewRequest{All: true})
	if err != nil || preview.Applied || len(preview.Candidates) != 1 || preview.Candidates[0].SourceSHA256 != source.Envelope.EnvelopeSHA256 {
		t.Fatalf("review preview: %+v %v", preview, err)
	}
	if count := tableRows(t, fresh, "redaction_projection_reviews"); count != 0 {
		t.Fatal("review preview wrote a review")
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("fixture-password")) || bytes.Contains(encoded, []byte("fixture-user")) {
		t.Fatal("review output exposed original credentials")
	}
	applied, err := fresh.syncer.ReviewRedactionProjections(ctx, RedactionReviewRequest{All: true, Apply: true, Reviewer: "engineer", Reason: "historical HTTPS credential"})
	if err != nil || !applied.Applied || len(applied.Candidates) != 1 {
		t.Fatalf("apply review: %+v %v", applied, err)
	}

	fresh.sync(t, OutcomeSynced)
	var payload, retained string
	if err := fresh.db.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='historical'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "fixture-password") || strings.Contains(payload, "fixture-user") {
		t.Fatal("operational row holds the unredacted source")
	}
	if err := fresh.db.QueryRow(`SELECT envelope_json FROM portable_envelopes WHERE record_type='checkpoint' AND record_id='historical'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != string(source.Bytes) {
		t.Fatal("source envelope not retained exactly")
	}
	// Repeated syncs replay exactly and never publish the projection as a new
	// version of the source identity.
	fresh.sync(t, OutcomeSynced)
	// The fixture origin never held the record operationally, so it is a second
	// fresh peer: it reviews for itself and syncs after the first one published.
	if _, err := origin.syncer.ReviewRedactionProjections(ctx, RedactionReviewRequest{Sources: []string{source.Envelope.EnvelopeSHA256}, Apply: true, Reviewer: "engineer", Reason: "historical HTTPS credential"}); err != nil {
		t.Fatal(err)
	}
	origin.sync(t, OutcomeSynced)
	fresh.sync(t, OutcomeSynced)
	if count := tableRows(t, origin, "checkpoints"); count != 1 {
		t.Fatalf("second fresh peer did not import the projection: %d", count)
	}
	if count := tableRows(t, fresh, "checkpoints"); count != 1 {
		t.Fatalf("repeated syncs changed checkpoints: %d", count)
	}
	freshRepo, err := fresh.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held, err := fresh.syncer.readTrackedObservations(ctx, freshRepo, testLineage, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	for hash, body := range held {
		spec, err := portable.DecodeMemoryEnvelope(body)
		if err != nil {
			t.Fatal(err)
		}
		if spec.RecordType == "checkpoint" && spec.RecordID == "historical" && hash != source.Envelope.EnvelopeSHA256 {
			t.Fatalf("a second version of the source identity was published: %s", hash)
		}
	}
}

func tableRows(t *testing.T, m machine, table string) int {
	t.Helper()
	var count int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
