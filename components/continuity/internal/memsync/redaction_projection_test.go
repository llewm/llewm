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
