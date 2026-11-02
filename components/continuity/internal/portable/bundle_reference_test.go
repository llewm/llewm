package portable

import (
	"bytes"
	"fmt"
)

// Sequential bundle validator before bounded parallel record checks.
func referenceValidateBundle(manifestBytes, recordsBytes []byte) (Bundle, error) {
	if len(manifestBytes) == 0 || len(manifestBytes) > MaxManifestBytes {
		return Bundle{}, fmt.Errorf("manifest size must be between 1 and %d bytes", MaxManifestBytes)
	}
	if len(recordsBytes) > MaxRecordsBytes {
		return Bundle{}, fmt.Errorf("records exceed %d bytes", MaxRecordsBytes)
	}
	canonicalManifest, err := CanonicalizeJSON(manifestBytes)
	if err != nil {
		return Bundle{}, fmt.Errorf("manifest is not canonical: %w", err)
	}
	if !bytes.Equal(canonicalManifest, manifestBytes) {
		return Bundle{}, fmt.Errorf("manifest is not canonical")
	}
	var manifest Manifest
	if err := strictDecode(manifestBytes, &manifest); err != nil {
		return Bundle{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.Format != "llewm-portable-memory" || manifest.FormatVersion != PortableFormatVersion || manifest.ExportMode != "complete" {
		return Bundle{}, fmt.Errorf("unsupported manifest format, version, or export mode")
	}
	if !portableIDPattern.MatchString(manifest.ExportID) || !lineagePattern.MatchString(manifest.LineageID) || !instancePattern.MatchString(manifest.SourceInstanceID) {
		return Bundle{}, fmt.Errorf("invalid manifest identity")
	}
	if err := validateCanonicalTime(manifest.CreatedAt); err != nil {
		return Bundle{}, fmt.Errorf("manifest created time: %w", err)
	}
	if manifest.SourceDatabaseSchema == "" || manifest.SelectionProfile == "" || manifest.RecordCounts == nil || manifest.Omissions == nil || !isHash(manifest.RedactionProfile.SHA256) || manifest.RedactionProfile.ID == "" || manifest.RedactionProfile.Version < 1 {
		return Bundle{}, fmt.Errorf("manifest has missing or invalid required fields")
	}
	for index, omission := range manifest.Omissions {
		if omission.RecordFamily == "" || omission.Reason == "" {
			return Bundle{}, fmt.Errorf("manifest omission has missing fields")
		}
		if index > 0 && !omissionLess(manifest.Omissions[index-1], omission) {
			return Bundle{}, fmt.Errorf("manifest omissions are duplicate or out of order")
		}
	}
	if manifest.RecordsSHA256 != hashHex(recordsBytes) {
		return Bundle{}, fmt.Errorf("records hash mismatch")
	}
	if len(recordsBytes) > 0 && recordsBytes[len(recordsBytes)-1] != '\n' {
		return Bundle{}, fmt.Errorf("records file must end with a newline")
	}
	lines := bytes.Split(recordsBytes, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > MaxRecordCount {
		return Bundle{}, fmt.Errorf("record count exceeds %d", MaxRecordCount)
	}
	records := make([]EncodedEnvelope, 0, len(lines))
	counts := make(map[string]int)
	identities := make(map[string]struct{})
	var setInput bytes.Buffer
	var previous *Envelope
	for position, line := range lines {
		if len(line) == 0 {
			return Bundle{}, fmt.Errorf("empty record line %d", position+1)
		}
		canonical, err := CanonicalizeJSON(line)
		if err != nil {
			return Bundle{}, fmt.Errorf("record line %d is not canonical: %w", position+1, err)
		}
		if !bytes.Equal(canonical, line) {
			return Bundle{}, fmt.Errorf("record line %d is not canonical", position+1)
		}
		var envelope Envelope
		if err := strictDecode(line, &envelope); err != nil {
			return Bundle{}, fmt.Errorf("decode record line %d: %w", position+1, err)
		}
		if err := validateEnvelope(envelope, manifest.LineageID); err != nil {
			return Bundle{}, fmt.Errorf("validate record line %d: %w", position+1, err)
		}
		if previous != nil && !envelopeLess(*previous, envelope) {
			return Bundle{}, fmt.Errorf("record line %d is duplicate or out of order", position+1)
		}
		identity := fmt.Sprintf("%s\x00%s\x00%d", envelope.RecordType, envelope.RecordID, envelope.RecordRevision)
		if _, exists := identities[identity]; exists {
			return Bundle{}, fmt.Errorf("duplicate record identity at line %d", position+1)
		}
		identities[identity] = struct{}{}
		counts[envelope.RecordType]++
		setInput.WriteString(envelope.EnvelopeSHA256)
		setInput.WriteByte('\n')
		records = append(records, EncodedEnvelope{Envelope: envelope, Bytes: append([]byte(nil), line...)})
		copy := envelope
		previous = &copy
	}
	if !equalCounts(counts, manifest.RecordCounts) {
		return Bundle{}, fmt.Errorf("record counts do not match manifest")
	}
	if manifest.RecordSetSHA256 != hashHex(setInput.Bytes()) {
		return Bundle{}, fmt.Errorf("record set hash mismatch")
	}
	return Bundle{Manifest: manifest, ManifestBytes: append([]byte(nil), manifestBytes...), RecordsBytes: append([]byte(nil), recordsBytes...), Records: records}, nil
}
