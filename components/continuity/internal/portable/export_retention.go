package portable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"io"
	"strings"
)

var errExportRetentionChanged = errors.New("canonical state changed outside selected export retention")

// The export writer may only insert its selected exact envelopes. Read the
// global journal range, since an unexpected trigger can also affect a different
// project. The reserved writer prevents unrelated concurrent commits here.
func verifyExportRetentionWithin(ctx context.Context, tx *sql.Tx, project string, marker int64, before, after string, inserted map[string]EncodedEnvelope) error {
	initialGlobal, _, _ := strings.Cut(before, ":")
	finalGlobal, _, _ := strings.Cut(after, ":")
	if marker < 0 || initialGlobal != finalGlobal || (len(inserted) == 0 && before != after) {
		return errExportRetentionChanged
	}
	remaining := make(map[string]bool, len(inserted))
	for key := range inserted {
		remaining[key] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,table_name,row_key,operation FROM history_sync_changes WHERE sequence>? ORDER BY sequence LIMIT ?`, marker, len(inserted)+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var owner, table, key, operation string
		if err := rows.Scan(&owner, &table, &key, &operation); err != nil {
			return err
		}
		syncwork.RecordWork(ctx, "export_retention_journal_rows_read", 1)
		if owner != project || table != "portable_envelopes" || operation != "insert" {
			return errExportRetentionChanged
		}
		decoder := json.NewDecoder(strings.NewReader(key))
		decoder.UseNumber()
		var parts []any
		var extra any
		if decoder.Decode(&parts) != nil || len(parts) != 4 || decoder.Decode(&extra) != io.EOF {
			return errExportRetentionChanged
		}
		kind, kindOK := parts[1].(string)
		id, idOK := parts[2].(string)
		number, numberOK := parts[3].(json.Number)
		revision, parseErr := number.Int64()
		if parts[0] != project || !kindOK || !idOK || !numberOK || parseErr != nil || revision < 0 || int64(int(revision)) != revision {
			return errExportRetentionChanged
		}
		identity := RecordKey(kind, id, int(revision))
		if !remaining[identity] {
			return errExportRetentionChanged
		}
		delete(remaining, identity)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(remaining) != 0 {
		return errExportRetentionChanged
	}
	// Journal rows alone cannot prove that an insert actually retained bytes.
	// Verify the durable projection even if an unexpected trigger forged an event.
	for _, object := range inserted {
		e := object.Envelope
		var body string
		err := tx.QueryRowContext(ctx, `SELECT envelope_json FROM portable_envelopes WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=?`, project, e.RecordType, e.RecordID, e.RecordRevision).Scan(&body)
		syncwork.RecordWork(ctx, "export_retention_rows_checked", 1)
		if errors.Is(err, sql.ErrNoRows) {
			return errExportRetentionChanged
		}
		if err != nil {
			return err
		}
		syncwork.RecordWork(ctx, "export_retention_bytes_checked", int64(len(body)))
		if !bytes.Equal([]byte(body), object.Bytes) {
			return errExportRetentionChanged
		}
	}
	return nil
}
