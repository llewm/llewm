package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// retainedSpecs overlays only records that still exist operationally. Keeping
// these bytes avoids applying a destination host's redaction to imported prose,
// and keeps local exports stable after a project move. It never creates records.
func retainedSpecs(ctx context.Context, source queryer, projectID, lineageID string, specs []EnvelopeSpec) ([]EnvelopeSpec, error) {
	rows, err := source.QueryContext(ctx, `SELECT record_type,record_id,record_revision,envelope_json FROM portable_envelopes WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type retainedRecord struct {
		spec EnvelopeSpec
		hash string
	}
	retained := map[string]retainedRecord{}
	for rows.Next() {
		var kind, id, body string
		var revision int
		if err := rows.Scan(&kind, &id, &revision, &body); err != nil {
			return nil, err
		}
		spec, err := DecodeMemoryEnvelope([]byte(body))
		if err != nil {
			return nil, fmt.Errorf("retained portable envelope: %w", err)
		}
		if spec.LineageID != lineageID || spec.RecordType != kind || spec.RecordID != id || spec.RecordRevision != revision {
			return nil, fmt.Errorf("retained portable envelope identity mismatch")
		}
		// DecodeMemoryEnvelope has already validated the canonical bytes and
		// envelope hash. Reuse that hash when checking recovery rather than
		// encoding the same retained record again.
		var envelope Envelope
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			return nil, fmt.Errorf("retained portable envelope: %w", err)
		}
		retained[RecordKey(kind, id, revision)] = retainedRecord{spec: spec, hash: envelope.EnvelopeSHA256}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, spec := range specs {
		if saved, ok := retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)]; ok {
			specs[i] = saved.spec
		}
	}
	recovered, err := RecoveredEnvelopes(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	for i, spec := range specs {
		hash := ""
		if saved, ok := retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)]; ok {
			hash = saved.hash
		} else {
			encoded, err := EncodeEnvelope(spec)
			if err != nil {
				return nil, err
			}
			hash = encoded.Envelope.EnvelopeSHA256
		}
		if original, ok := recovered[hash]; ok {
			specs[i], err = DecodeMemoryEnvelope(original.Bytes)
			if err != nil {
				return nil, err
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
