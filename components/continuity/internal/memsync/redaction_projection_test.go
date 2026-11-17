package memsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	for hash, observation := range held {
		spec, err := portable.DecodeMemoryEnvelope(observation.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if spec.RecordType == "checkpoint" && spec.RecordID == "historical" && hash != source.Envelope.EnvelopeSHA256 {
			t.Fatalf("a second version of the source identity was published: %s", hash)
		}
	}
}

func TestReviewRedactionClassifiesTrackedObservations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newMachine(t, "projection-classification", bareRemote(t))
	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	encode := func(kind, id, authority, payload string, refs ...portable.Reference) portable.EncodedEnvelope {
		t.Helper()
		e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{
			LineageID: testLineage, RecordType: kind, RecordSchemaVersion: 1, RecordID: id,
			RecordedAt: at, Authority: authority, Payload: json.RawMessage(payload), Refs: refs,
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	link := func(id string) portable.EncodedEnvelope {
		t.Helper()
		return encode("work_narrative_supersession", id, "mixed", fmt.Sprintf(`{"successor_id":%q,"supersedes_id":"original"}`, id),
			portable.Reference{Kind: "narrative", ID: id}, portable.Reference{Kind: "narrative", ID: "original"})
	}
	narrative := `{"title":"Narrative","trigger":"manual","claims":[{"kind":"note","text":"Observation","authority":"caller_asserted"}],"anchors":[],"creator":"engineer","operational_schema_version":1}`
	repairableSpec := portable.EnvelopeSpec{
		LineageID: testLineage, RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: "repairable",
		RecordedAt: at, Authority: "caller_asserted",
		Index:   json.RawMessage(`{"title":"Historical","status":"completed"}`),
		Payload: json.RawMessage(`{"task":"Historical","status":"completed","summary":"https://fixture-user:fixture-password@example.test/history","next_action":"Done","operational_schema_version":1}`),
	}
	repairable, err := portable.EncodeEnvelope(repairableSpec)
	if err != nil {
		t.Fatal(err)
	}
	broken := encode("checkpoint", "unrepairable", "caller_asserted", `{"task":"Historical","status":"invalid"}`)
	// Retired and future record types still have valid wire envelopes. The
	// semantic encoder deliberately refuses to construct them.
	retype := func(kind string) portable.EncodedEnvelope {
		t.Helper()
		original := string(broken.Bytes)
		hashField := fmt.Sprintf(`"envelope_sha256":"%s",`, broken.Envelope.EnvelopeSHA256)
		unsigned := strings.Replace(original, hashField, "", 1)
		unsigned = strings.Replace(unsigned, `"record_type":"checkpoint"`, fmt.Sprintf(`"record_type":%q`, kind), 1)
		signed := fmt.Sprintf(`"envelope_sha256":"%x",`, sha256.Sum256([]byte(unsigned)))
		body := strings.Replace(unsigned, `"format":`, signed+`"format":`, 1)
		e, err := portable.DecodeTransportEnvelope([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	invalidSuccessor := strings.Replace(narrative, `"trigger":"manual"`, `"trigger":"invalid"`, 1)
	block, err := recordsync.EncodeBlock(testLineage, []portable.EncodedEnvelope{
		retype("contract"), retype("future_record"), repairable, broken,
		link("valid"), link("missing"), link("invalid"),
		encode("work_narrative", "valid", "mixed", narrative),
		encode("work_narrative", "invalid", "mixed", invalidSuccessor),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitBlocks(ctx, repo, testLineage, []recordsync.Block{block}, "fixture: mixed tracked observations", false); err != nil {
		t.Fatal(err)
	}
	result, err := m.syncer.ReviewRedactionProjections(ctx, RedactionReviewRequest{All: true})
	if err != nil || result.Applied || result.SkippedRetired != 1 ||
		result.ValidSupersessions != 1 || result.InvalidSupersessions != 2 ||
		result.Unsupported != 2 || result.Unrepairable != 1 ||
		len(result.Candidates) != 1 || result.Candidates[0].SourceSHA256 != repairable.Envelope.EnvelopeSHA256 {
		t.Fatalf("review counts: %+v %v", result, err)
	}
	if count := tableRows(t, m, "redaction_projection_reviews"); count != 0 {
		t.Fatal("review preview wrote a review")
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
