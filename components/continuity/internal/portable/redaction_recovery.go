package portable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// RedactionRecovery is a correction to transport interpretation. Both observed
// envelopes remain immutable; Original is the form before an additional host's
// root/ID substitutions. It grants nothing and changes no operational record.
type RedactionRecovery struct {
	Original    json.RawMessage `json:"original"`
	Derived     json.RawMessage `json:"derived"`
	ProjectRoot string          `json:"project_root"`
	ProjectID   string          `json:"project_id"`
	Creator     string          `json:"creator"`
	Reason      string          `json:"reason"`
}

func recoveryEnvelope(body []byte) (EncodedEnvelope, error) {
	spec, err := DecodeMemoryEnvelope(body)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	switch spec.RecordType {
	case "checkpoint", "work_narrative", "procedure", "track_plan":
	default:
		return EncodedEnvelope{}, fmt.Errorf("redaction recovery does not support %s", spec.RecordType)
	}
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return encoded, err
	}
	if _, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{encoded}}); err != nil {
		return encoded, err
	}
	return encoded, nil
}

// RedactionRecoverySpec validates an exact, one-way root/ID transformation.
// The canonical ID binds both observations and the explanation. It cannot
// resolve arbitrary content differences or reverse a lossy substitution.
func RedactionRecoverySpec(value RedactionRecovery, at time.Time) (EnvelopeSpec, error) {
	original, err := recoveryEnvelope(value.Original)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	derived, err := recoveryEnvelope(value.Derived)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	if original.Envelope.ContentSHA256 == derived.Envelope.ContentSHA256 {
		return EnvelopeSpec{}, fmt.Errorf("redaction recovery requires changed payload content")
	}
	if value.ProjectRoot == "" && value.ProjectID == "" {
		return EnvelopeSpec{}, fmt.Errorf("a redacted project root or ID is required")
	}
	if value.ProjectRoot != "" && (!filepath.IsAbs(value.ProjectRoot) || filepath.Clean(value.ProjectRoot) != value.ProjectRoot || value.ProjectRoot == "/") {
		return EnvelopeSpec{}, fmt.Errorf("redacted project root must be an exact absolute project path")
	}
	if value.ProjectID != "" && (!strings.HasPrefix(value.ProjectID, "project-") || len(value.ProjectID) < 16) {
		return EnvelopeSpec{}, fmt.Errorf("redacted project ID is invalid")
	}
	for _, v := range []string{value.ProjectRoot, value.ProjectID, value.Creator, value.Reason} {
		if redact.String(v) != v || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") || len(v) > 4096 {
			return EnvelopeSpec{}, fmt.Errorf("recovery context must be bounded, redacted text")
		}
	}
	if value.Creator == "" || value.Reason == "" {
		return EnvelopeSpec{}, fmt.Errorf("recovery creator and reason are required")
	}
	// Only transport content, its index and references may have been redacted.
	spec, err := DecodeMemoryEnvelope(original.Bytes)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	transform := func(s string) string {
		if value.ProjectRoot != "" {
			s = strings.ReplaceAll(s, value.ProjectRoot, "[PROJECT_ROOT]")
		}
		if value.ProjectID != "" {
			s = strings.ReplaceAll(s, value.ProjectID, "[PROJECT_ID]")
		}
		return s
	}
	if spec.Payload, err = recoveryJSON(spec.Payload, transform); err != nil {
		return EnvelopeSpec{}, err
	}
	if len(spec.Index) > 0 {
		if spec.Index, err = recoveryJSON(spec.Index, transform); err != nil {
			return EnvelopeSpec{}, err
		}
	}
	for i := range spec.Refs {
		spec.Refs[i].ID = transform(spec.Refs[i].ID)
	}
	expected, err := EncodeEnvelope(spec)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	if bytes.Equal(original.Bytes, derived.Bytes) || !bytes.Equal(expected.Bytes, derived.Bytes) {
		return EnvelopeSpec{}, fmt.Errorf("observations are not the exact declared additional redaction")
	}
	value.Original, value.Derived = original.Bytes, derived.Bytes
	body, err := json.Marshal(value)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	canonical, err := CanonicalizeJSON(body)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	return EnvelopeSpec{LineageID: original.Envelope.LineageID, RecordType: "redaction_recovery", RecordSchemaVersion: 1, RecordID: "redaction-" + hashHex(canonical), RecordedAt: at, Authority: "caller_asserted", Refs: []Reference{}, Payload: canonical}, nil
}

func recoveryJSON(body []byte, transform func(string) string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return transform(x), nil
		case []any:
			for i := range x {
				var err error
				x[i], err = walk(x[i])
				if err != nil {
					return nil, err
				}
			}
			return x, nil
		case map[string]any:
			out := map[string]any{}
			for k, v := range x {
				key := transform(k)
				if _, exists := out[key]; exists {
					return nil, fmt.Errorf("redaction collapses object keys")
				}
				next, err := walk(v)
				if err != nil {
					return nil, err
				}
				out[key] = next
			}
			return out, nil
		default:
			return v, nil
		}
	}
	clean, err := walk(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(clean)
}

func decodeRedactionRecovery(envelope EncodedEnvelope) (RedactionRecovery, error) {
	var value RedactionRecovery
	if err := strictDecode(envelope.Envelope.Payload, &value); err != nil {
		return value, err
	}
	at, err := parsePortableTime(envelope.Envelope.RecordedAt)
	if err != nil {
		return value, err
	}
	spec, err := RedactionRecoverySpec(value, at)
	if err != nil {
		return value, err
	}
	expected, err := EncodeEnvelope(spec)
	if err != nil {
		return value, err
	}
	if !bytes.Equal(expected.Bytes, envelope.Bytes) {
		return value, fmt.Errorf("redaction recovery envelope does not match proof")
	}
	return value, nil
}

func loadRedactionRecoverySpecs(ctx context.Context, source queryer, projectID string) ([]EnvelopeSpec, error) {
	rows, err := source.QueryContext(ctx, `SELECT envelope_json FROM redaction_recoveries WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var specs []EnvelopeSpec
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		spec, err := DecodeMemoryEnvelope([]byte(body))
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

func appendRedactionRecovery(ctx context.Context, tx *sql.Tx, projectID string, encoded EncodedEnvelope) error {
	value, err := decodeRedactionRecovery(encoded)
	if err != nil {
		return err
	}
	original, _ := recoveryEnvelope(value.Original)
	derived, _ := recoveryEnvelope(value.Derived)
	o, d := original.Envelope, derived.Envelope
	_, err = tx.ExecContext(ctx, `INSERT INTO redaction_recoveries(project_id,id,record_type,record_id,record_revision,original_sha256,derived_sha256,original_content_sha256,derived_content_sha256,envelope_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, projectID, encoded.Envelope.RecordID, o.RecordType, o.RecordID, o.RecordRevision, o.EnvelopeSHA256, d.EnvelopeSHA256, o.ContentSHA256, d.ContentSHA256, string(encoded.Bytes))
	return err
}

// RecoveredEnvelopes returns the corrections keyed by derived envelope hash.
// Ambiguous or chained corrections refuse rather than choosing an observation.
func RecoveredEnvelopes(ctx context.Context, source queryer, projectID string) (map[string]EncodedEnvelope, error) {
	specs, err := loadRedactionRecoverySpecs(ctx, source, projectID)
	if err != nil {
		return nil, err
	}
	result := map[string]EncodedEnvelope{}
	for _, spec := range specs {
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		value, err := decodeRedactionRecovery(encoded)
		if err != nil {
			return nil, err
		}
		o, _ := recoveryEnvelope(value.Original)
		d, _ := recoveryEnvelope(value.Derived)
		if held, ok := result[d.Envelope.EnvelopeSHA256]; ok && held.Envelope.EnvelopeSHA256 != o.Envelope.EnvelopeSHA256 {
			return nil, fmt.Errorf("ambiguous redaction recovery")
		}
		result[d.Envelope.EnvelopeSHA256] = o
	}
	for _, o := range result {
		if _, chain := result[o.Envelope.EnvelopeSHA256]; chain {
			return nil, fmt.Errorf("chained redaction recovery requires explicit review")
		}
	}
	return result, nil
}
