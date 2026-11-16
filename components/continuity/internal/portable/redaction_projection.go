package portable

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// RedactionProjection is a safe interpretation, never a replacement observation.
// The source is identified by its exact transport hash; its bytes are deliberately
// absent from the result so preview output cannot repeat a historical credential.
type RedactionProjection struct {
	SourceSHA256 string          `json:"source_sha256"`
	Projected    EncodedEnvelope `json:"-"`
}

// ErrSourceAdmissible reports that a source needs no projection: it already
// passes current admission checks and imports as itself.
var ErrSourceAdmissible = errors.New("projection source already passes current admission checks")

// ErrProjectionUnsupported reports a family that redaction projection never
// rewrites. Such a source failing alone may also be a record, like a
// supersession, that only decodes together with its dependencies.
var ErrProjectionUnsupported = errors.New("redaction projection does not support")

// PreviewRedactionProjection admits only the prose families with observed legacy
// redaction failures. Identity, authority, timestamps and semantic references may
// not change. Current ordinary decoding remains the final admission gate.
// This function does not authorize an import or write any state.
func PreviewRedactionProjection(source []byte) (RedactionProjection, error) {
	spec, err := DecodeMemoryEnvelope(source)
	if err != nil {
		return RedactionProjection{}, fmt.Errorf("invalid projection source transport")
	}
	original, err := EncodeEnvelope(spec)
	if err != nil {
		return RedactionProjection{}, fmt.Errorf("invalid projection source envelope")
	}
	if _, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{original}}); err == nil {
		return RedactionProjection{}, ErrSourceAdmissible
	}
	switch spec.RecordType {
	case "checkpoint", "checkpoint_action", "decision", "resource_note":
	default:
		return RedactionProjection{}, fmt.Errorf("%w: %s", ErrProjectionUnsupported, spec.RecordType)
	}
	// Preserve JSON numbers exactly, and refuse key collapse instead of allowing
	// structural sanitization to overwrite one field with another.
	spec.Payload, err = projectionJSON(spec.Payload)
	if err != nil {
		return RedactionProjection{}, err
	}
	if len(spec.Index) != 0 {
		spec.Index, err = projectionJSON(spec.Index)
		if err != nil {
			return RedactionProjection{}, err
		}
	}
	// Evidence prose may contain a URL; actual identity/dependency references
	// cannot be renamed by a redaction projection.
	for i := range spec.Refs {
		clean := redact.String(spec.Refs[i].ID)
		if clean != spec.Refs[i].ID && spec.Refs[i].Kind != "evidence_ref" {
			return RedactionProjection{}, fmt.Errorf("projection would change a semantic reference")
		}
		spec.Refs[i].ID = clean
	}
	projected, err := EncodeEnvelope(spec)
	if err != nil {
		return RedactionProjection{}, fmt.Errorf("invalid projected transport")
	}
	if bytes.Equal(original.Bytes, projected.Bytes) {
		return RedactionProjection{}, fmt.Errorf("source failure is not repaired by redaction")
	}
	if _, err := decodeOperationalBundle(Bundle{Records: []EncodedEnvelope{projected}}); err != nil {
		// Do not echo decoder details: source identity or unsupported data could
		// contain a credential even when the source transport itself is canonical.
		return RedactionProjection{}, fmt.Errorf("redacted projection still fails current admission checks")
	}
	return RedactionProjection{SourceSHA256: original.Envelope.EnvelopeSHA256, Projected: projected}, nil
}

func projectionJSON(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid projection JSON")
	}
	var check func(any) error
	check = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			keys := map[string]bool{}
			for k, child := range x {
				clean := redact.String(k)
				if keys[clean] {
					return fmt.Errorf("redaction collapses object keys")
				}
				keys[clean] = true
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(value); err != nil {
		return nil, err
	}
	clean := redact.Value(value)
	if !reflect.DeepEqual(clean, redact.Value(clean)) {
		return nil, fmt.Errorf("projection redaction is not idempotent")
	}
	return json.Marshal(clean)
}
