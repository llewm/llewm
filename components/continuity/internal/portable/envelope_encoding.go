package portable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// marshalCanonicalEnvelope writes the fixed envelope layout after EncodeEnvelope
// has canonicalized and validated its payload, index and references. It is not a
// reader or a general canonicalizer: untrusted nested JSON must still go through
// CanonicalizeJSON. Keep the generic encoder as the independent wire reference.
func marshalCanonicalEnvelope(e Envelope) ([]byte, error) {
	// Preserve the general canonicalizer's limit on the intermediate JSON,
	// including HTML-escape expansion, and its interoperable integer range.
	intermediate, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(intermediate) > MaxCanonicalBytes {
		return nil, fmt.Errorf("canonical JSON exceeds %d bytes", MaxCanonicalBytes)
	}
	if int64(e.RecordRevision) > 1<<53-1 {
		return nil, fmt.Errorf("integer %d exceeds interoperable range", e.RecordRevision)
	}
	var output bytes.Buffer
	output.Grow(len(intermediate))
	output.WriteString(`{"authority":`)
	writeCanonicalString(&output, e.Authority)
	output.WriteString(`,"content_sha256":`)
	writeCanonicalString(&output, e.ContentSHA256)
	output.WriteString(`,"envelope_sha256":`)
	writeCanonicalString(&output, e.EnvelopeSHA256)
	output.WriteString(`,"format":`)
	writeCanonicalString(&output, e.Format)
	output.WriteString(`,"format_version":`)
	output.WriteString(strconv.Itoa(e.FormatVersion))
	if len(e.Index) > 0 {
		output.WriteString(`,"index":`)
		output.Write(e.Index)
	}
	output.WriteString(`,"lineage_id":`)
	writeCanonicalString(&output, e.LineageID)
	output.WriteString(`,"payload":`)
	output.Write(e.Payload)
	output.WriteString(`,"record_id":`)
	writeCanonicalString(&output, e.RecordID)
	output.WriteString(`,"record_revision":`)
	output.WriteString(strconv.Itoa(e.RecordRevision))
	output.WriteString(`,"record_schema_version":`)
	output.WriteString(strconv.Itoa(e.RecordSchemaVersion))
	output.WriteString(`,"record_type":`)
	writeCanonicalString(&output, e.RecordType)
	output.WriteString(`,"recorded_at":`)
	writeCanonicalString(&output, e.RecordedAt)
	if len(e.Refs) > 0 {
		output.WriteString(`,"refs":[`)
		for i, ref := range e.Refs {
			if i > 0 {
				output.WriteByte(',')
			}
			output.WriteString(`{"id":`)
			writeCanonicalString(&output, ref.ID)
			output.WriteString(`,"kind":`)
			writeCanonicalString(&output, ref.Kind)
			output.WriteByte('}')
		}
		output.WriteByte(']')
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}
