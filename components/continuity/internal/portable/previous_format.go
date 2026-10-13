package portable

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// ConvertPreviousEnvelope validates an original format-2 observation before
// re-encoding it for format 3. Ordinary imports accept only the current format.
// Payloads, record identity, revision, time and references remain unchanged.
func ConvertPreviousEnvelope(line []byte) (EncodedEnvelope, error) {
	var value Envelope
	if err := strictDecode(line, &value); err != nil {
		return EncodedEnvelope{}, fmt.Errorf("decode format-2 envelope: %w", err)
	}
	if err := validateEnvelopeFormat(value, value.LineageID, "llmem-portable-record", 2); err != nil {
		return EncodedEnvelope{}, fmt.Errorf("validate format-2 envelope: %w", err)
	}
	canonical, err := canonicalMarshal(value)
	if err != nil || !bytes.Equal(canonical, line) {
		return EncodedEnvelope{}, fmt.Errorf("format-2 envelope is not canonical")
	}
	// Re-encode the validated transport object itself. Retired record payloads
	// intentionally have no operational encoder, but must remain in the archive.
	value.Format, value.FormatVersion, value.EnvelopeSHA256 = "llewm-portable-record", PortableFormatVersion, ""
	withoutHash, err := canonicalMarshal(value)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	value.EnvelopeSHA256 = hashHex(withoutHash)
	encoded, err := canonicalMarshal(value)
	return EncodedEnvelope{Envelope: value, Bytes: encoded}, err
}

// ConvertPreviousMemoryFile also proves that an old observation occupied its
// exact content-derived path. Invalid/unknown records refuse conversion.
func ConvertPreviousMemoryFile(relative string, body []byte) (EncodedEnvelope, error) {
	line, err := oneReviewedEnvelope(relative, body)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	encoded, err := ConvertPreviousEnvelope(line)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	var previous Envelope
	if err := strictDecode(line, &previous); err != nil {
		return EncodedEnvelope{}, err
	}
	want := strings.Replace(filepath.ToSlash(MemoryRecordPath(previous)), MemoryRepositoryRoot+"/", FormerMemoryRepositoryRoot+"/", 1)
	if filepath.ToSlash(relative) != want {
		return EncodedEnvelope{}, fmt.Errorf("format-2 observation is stored at the wrong path: %s", relative)
	}
	return encoded, nil
}
