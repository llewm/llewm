package portable

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// snapshotEncodings holds only encodings validated while loading the same
// operational snapshot. It must not be reused after changing specs or across
// transactions; no database contents are trusted without validation.
type snapshotEncodings map[string]EncodedEnvelope

func (validated snapshotEncodings) encode(ctx context.Context, spec EnvelopeSpec) (EncodedEnvelope, error) {
	if encoded, ok := validated[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)]; ok {
		syncwork.RecordWork(ctx, "snapshot_encoding_reuses", 1)
		return encoded, nil
	}
	syncwork.RecordWork(ctx, "snapshot_envelope_encodes", 1)
	return EncodeEnvelope(spec)
}

// retainedSpecs overlays only records that still exist operationally. Keeping
// these bytes avoids applying a destination host's redaction to imported prose,
// and keeps local exports stable after a project move. It never creates records.
func retainedSpecs(ctx context.Context, source queryer, projectID, lineageID string, specs []EnvelopeSpec, validated snapshotEncodings) ([]EnvelopeSpec, error) {
	rows, err := source.QueryContext(ctx, `SELECT record_type,record_id,record_revision,envelope_json FROM portable_envelopes WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type retainedRecord struct {
		spec    EnvelopeSpec
		encoded EncodedEnvelope
	}
	retained := map[string]retainedRecord{}
	for rows.Next() {
		var kind, id, body string
		var revision int
		if err := rows.Scan(&kind, &id, &revision, &body); err != nil {
			return nil, err
		}
		syncwork.RecordWork(ctx, "retained_envelopes_decoded", 1)
		syncwork.RecordWork(ctx, "retained_envelope_bytes_decoded", int64(len(body)))
		spec, encoded, err := decodeMemoryEnvelope([]byte(body))
		if err != nil {
			return nil, fmt.Errorf("retained portable envelope: %w", err)
		}
		if spec.LineageID != lineageID || spec.RecordType != kind || spec.RecordID != id || spec.RecordRevision != revision {
			return nil, fmt.Errorf("retained portable envelope identity mismatch")
		}
		retained[RecordKey(kind, id, revision)] = retainedRecord{spec: spec, encoded: encoded}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, spec := range specs {
		if saved, ok := retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)]; ok {
			specs[i] = saved.spec
			if validated != nil {
				validated[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] = saved.encoded
			}
		}
	}
	recovered, err := RecoveredEnvelopes(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	for i, spec := range specs {
		hash := ""
		if saved, ok := retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)]; ok {
			hash = saved.encoded.Envelope.EnvelopeSHA256
		} else {
			syncwork.RecordWork(ctx, "snapshot_envelope_encodes", 1)
			encoded, err := EncodeEnvelope(spec)
			if err != nil {
				return nil, err
			}
			hash = encoded.Envelope.EnvelopeSHA256
			if validated != nil {
				validated[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] = encoded
			}
		}
		if original, ok := recovered[hash]; ok {
			var encoded EncodedEnvelope
			syncwork.RecordWork(ctx, "recovery_envelopes_decoded", 1)
			syncwork.RecordWork(ctx, "recovery_envelope_bytes_decoded", int64(len(original.Bytes)))
			specs[i], encoded, err = decodeMemoryEnvelope(original.Bytes)
			if err != nil {
				return nil, err
			}
			if validated != nil {
				validated[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] = encoded
			}
		}
	}
	return specs, nil
}

func retainEnvelope(ctx context.Context, tx *sql.Tx, projectID, origin string, encoded EncodedEnvelope) error {
	e := encoded.Envelope
	_, err := tx.ExecContext(ctx, `INSERT INTO portable_envelopes(project_id,record_type,record_id,record_revision,envelope_json,origin) VALUES(?,?,?,?,?,?) ON CONFLICT(project_id,record_type,record_id,record_revision) DO NOTHING`, projectID, e.RecordType, e.RecordID, e.RecordRevision, string(encoded.Bytes), origin)
	return err
}
