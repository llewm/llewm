package portable

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

func verifyCanonicalJournalSelection(t *testing.T, ctx context.Context, db *sql.DB, project, consumer, state string, expected []EncodedEnvelope) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND 0`, project); err != nil {
		t.Fatal(err)
	}
	before, changes, reason, err := database.ReadSyncChangesWithin(ctx, tx, project, consumer, state, 4096)
	if err != nil || reason != "" {
		t.Fatalf("journal selection input: %q %v", reason, err)
	}
	selected, used, err := CanonicalJournalEnvelopesTx(ctx, tx, project, changes, RecoveryComparisonComplete)
	if err != nil || !used || len(selected) != len(expected) {
		t.Fatalf("journal selection: got=%d expected=%d used=%t %v", len(selected), len(expected), used, err)
	}
	want := map[string][]byte{}
	for _, object := range expected {
		want[recordKey(object.Envelope)] = object.Bytes
	}
	for _, object := range selected {
		if !bytes.Equal(object.Bytes, want[recordKey(object.Envelope)]) {
			t.Fatalf("journal selection changed wire bytes: %s", recordKey(object.Envelope))
		}
	}
	after, err := database.SyncSnapshot(ctx, tx, project, consumer)
	if err != nil || before.Generation != after.Generation || before.Sequence != after.Sequence {
		t.Fatalf("read-only journal selection wrote canonical state: %+v/%+v %v", before, after, err)
	}
}
