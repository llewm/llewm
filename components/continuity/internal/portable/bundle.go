package portable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	PortableFormatVersion = 3
	MaxPayloadBytes       = 128 << 10
	MaxIndexBytes         = 16 << 10
	MaxManifestBytes      = 1 << 20
	MaxRecordsBytes       = 64 << 20
	MaxRecordCount        = 100000
)

var portableIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}:[A-Za-z0-9._:-]{1,256}$`)

// RetiredRecordType reports whether the type was deliberately cut: builds
// skip such records in the memory repository without counting them as
// needing a newer build.
func RetiredRecordType(recordType string) bool { return retiredRecordTypes[recordType] }

// retiredRecordTypes were cut in 6f: contracts became marked Markdown
// sections, so contract revisions, bindings, and transitions no longer travel.
var retiredRecordTypes = map[string]bool{"contract": true, "contract_binding": true, "contract_transition": true}

// SupportedRecordType reports whether this build can read records of the type.
func SupportedRecordType(recordType string) bool {
	_, supported := supportedRecordSchemas[recordType]
	return supported
}

var supportedRecordSchemas = map[string]int{
	"redaction_recovery": 1,
	"checkpoint":         1, "checkpoint_action": 1, "checkpoint_action_transition": 1,
	"roadmap_node": 2, "roadmap_binding": 1, "roadmap_link": 1,
	"roadmap_review_routine": 1, "roadmap_sync": 1,
	"roadmap_release": 1,
	"work_narrative":  1, "work_narrative_supersession": 1,
	"work_item": 1, "work_item_disposition": 1, "work_item_link": 1,
	"track_plan":                   1,
	"actor_invocation":             1,
	"decision":                     1,
	"topic_revision":               1,
	"topic_alias":                  1,
	"topic_link":                   1,
	"procedure":                    1,
	"resource":                     1,
	"resource_note":                1,
	"pin":                          1,
	"coverage_cell":                1,
	"lease_stamp":                  1,
	"stale_write_resolution":       1,
	"usage_session_summary":        1,
	"checkpoint_context":           1,
	"checkpoint_branches":          1,
	"fork_resolution":              1,
	"revision_fork_resolution":     1,
	"supersession_fork_resolution": 1,
}

type Reference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type EnvelopeSpec struct {
	LineageID           string
	RecordType          string
	RecordSchemaVersion int
	RecordID            string
	RecordRevision      int
	RecordedAt          time.Time
	Authority           string
	Index               json.RawMessage
	Refs                []Reference
	Payload             json.RawMessage
}

type Envelope struct {
	Format              string          `json:"format"`
	FormatVersion       int             `json:"format_version"`
	LineageID           string          `json:"lineage_id"`
	RecordType          string          `json:"record_type"`
	RecordSchemaVersion int             `json:"record_schema_version"`
	RecordID            string          `json:"record_id"`
	RecordRevision      int             `json:"record_revision"`
	RecordedAt          string          `json:"recorded_at"`
	Authority           string          `json:"authority"`
	ContentSHA256       string          `json:"content_sha256"`
	EnvelopeSHA256      string          `json:"envelope_sha256,omitempty"`
	Index               json.RawMessage `json:"index,omitempty"`
	Refs                []Reference     `json:"refs,omitempty"`
	Payload             json.RawMessage `json:"payload"`
}

type EncodedEnvelope struct {
	Envelope Envelope
	Bytes    []byte
}

type RedactionProfile struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
}

type Omission struct {
	RecordFamily string `json:"record_family"`
	Reason       string `json:"reason"`
}

type BundleSpec struct {
	ExportID             string
	LineageID            string
	SourceInstanceID     string
	CreatedAt            time.Time
	SourceDatabaseSchema string
	SelectionProfile     string
	RedactionProfile     RedactionProfile
	Omissions            []Omission
}

type Manifest struct {
	Format               string           `json:"format"`
	FormatVersion        int              `json:"format_version"`
	ExportID             string           `json:"export_id"`
	LineageID            string           `json:"lineage_id"`
	SourceInstanceID     string           `json:"source_instance_id"`
	CreatedAt            string           `json:"created_at"`
	SourceDatabaseSchema string           `json:"source_database_schema"`
	ExportMode           string           `json:"export_mode"`
	SelectionProfile     string           `json:"selection_profile"`
	RedactionProfile     RedactionProfile `json:"redaction_profile"`
	RecordCounts         map[string]int   `json:"record_counts"`
	RecordsSHA256        string           `json:"records_sha256"`
	RecordSetSHA256      string           `json:"record_set_sha256"`
	Omissions            []Omission       `json:"omissions"`
}

type Bundle struct {
	Manifest      Manifest
	ManifestBytes []byte
	RecordsBytes  []byte
	Records       []EncodedEnvelope
}

func EncodeEnvelope(spec EnvelopeSpec) (EncodedEnvelope, error) {
	if !lineagePattern.MatchString(spec.LineageID) {
		return EncodedEnvelope{}, fmt.Errorf("invalid lineage ID")
	}
	if strings.TrimSpace(spec.RecordType) == "" || strings.TrimSpace(spec.RecordID) == "" || strings.TrimSpace(spec.Authority) == "" {
		return EncodedEnvelope{}, fmt.Errorf("record type, record ID, and authority are required")
	}
	if !supportedRecordSchema(spec.RecordType, spec.RecordSchemaVersion) {
		return EncodedEnvelope{}, fmt.Errorf("unsupported record schema %s version %d", spec.RecordType, spec.RecordSchemaVersion)
	}
	if spec.RecordSchemaVersion < 1 || spec.RecordRevision < 0 {
		return EncodedEnvelope{}, fmt.Errorf("record schema version must be positive and revision non-negative")
	}
	if spec.RecordedAt.IsZero() {
		return EncodedEnvelope{}, fmt.Errorf("recorded time is required")
	}
	if len(spec.Payload) > MaxPayloadBytes {
		return EncodedEnvelope{}, fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
	}
	payload, err := canonicalObject("payload", spec.Payload)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	var index []byte
	if len(spec.Index) > 0 {
		if len(spec.Index) > MaxIndexBytes {
			return EncodedEnvelope{}, fmt.Errorf("index exceeds %d bytes", MaxIndexBytes)
		}
		index, err = canonicalObject("index", spec.Index)
		if err != nil {
			return EncodedEnvelope{}, err
		}
		if err := validateIndex(index); err != nil {
			return EncodedEnvelope{}, err
		}
	}
	refs := append([]Reference(nil), spec.Refs...)
	for _, reference := range refs {
		if strings.TrimSpace(reference.Kind) == "" || strings.TrimSpace(reference.ID) == "" {
			return EncodedEnvelope{}, fmt.Errorf("reference kind and ID are required")
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return lessUTF16(refs[i].Kind, refs[j].Kind)
		}
		return lessUTF16(refs[i].ID, refs[j].ID)
	})
	for index := 1; index < len(refs); index++ {
		if !referenceLess(refs[index-1], refs[index]) {
			return EncodedEnvelope{}, fmt.Errorf("references must be unique")
		}
	}
	value := Envelope{
		Format: "llewm-portable-record", FormatVersion: PortableFormatVersion,
		LineageID: spec.LineageID, RecordType: spec.RecordType,
		RecordSchemaVersion: spec.RecordSchemaVersion, RecordID: spec.RecordID,
		RecordRevision: spec.RecordRevision, RecordedAt: spec.RecordedAt.UTC().Format(time.RFC3339Nano),
		Authority: spec.Authority, ContentSHA256: hashHex(payload), Index: index,
		Refs: refs, Payload: payload,
	}
	// Reserve the final hash's exact width so canonicalization and its size
	// checks run once. Removing this member yields the canonical hash input:
	// authority and content_sha256 precede it; all nested objects follow it.
	const placeholder = "0000000000000000000000000000000000000000000000000000000000000000"
	const hashMember = `,"envelope_sha256":"` + placeholder + `"`
	value.EnvelopeSHA256 = placeholder
	encoded, err := canonicalMarshal(value)
	if err != nil {
		return EncodedEnvelope{}, err
	}
	memberStart := bytes.Index(encoded, []byte(hashMember))
	if memberStart < 0 {
		return EncodedEnvelope{}, fmt.Errorf("canonical envelope hash member is missing")
	}
	hash := sha256.New()
	hash.Write(encoded[:memberStart])
	hash.Write(encoded[memberStart+len(hashMember):])
	value.EnvelopeSHA256 = hex.EncodeToString(hash.Sum(nil))
	hashStart := memberStart + len(`,"envelope_sha256":"`)
	copy(encoded[hashStart:hashStart+len(placeholder)], value.EnvelopeSHA256)
	return EncodedEnvelope{Envelope: value, Bytes: encoded}, nil
}

func BuildBundle(spec BundleSpec, records []EnvelopeSpec) (Bundle, error) {
	if !portableIDPattern.MatchString(spec.ExportID) || !lineagePattern.MatchString(spec.LineageID) || !instancePattern.MatchString(spec.SourceInstanceID) {
		return Bundle{}, fmt.Errorf("invalid export, lineage, or source instance ID")
	}
	if spec.CreatedAt.IsZero() || strings.TrimSpace(spec.SourceDatabaseSchema) == "" || strings.TrimSpace(spec.SelectionProfile) == "" {
		return Bundle{}, fmt.Errorf("created time, source schema, and selection profile are required")
	}
	if spec.RedactionProfile.ID == "" || spec.RedactionProfile.Version < 1 || !isHash(spec.RedactionProfile.SHA256) {
		return Bundle{}, fmt.Errorf("invalid redaction profile")
	}
	sorted := append([]EnvelopeSpec(nil), records...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].RecordType != sorted[j].RecordType {
			return lessUTF16(sorted[i].RecordType, sorted[j].RecordType)
		}
		if sorted[i].RecordID != sorted[j].RecordID {
			return lessUTF16(sorted[i].RecordID, sorted[j].RecordID)
		}
		return sorted[i].RecordRevision < sorted[j].RecordRevision
	})
	encoded := make([]EncodedEnvelope, 0, len(sorted))
	counts := make(map[string]int)
	identities := make(map[string]struct{})
	var recordsBytes, setInput bytes.Buffer
	for _, record := range sorted {
		if record.LineageID != spec.LineageID {
			return Bundle{}, fmt.Errorf("record lineage does not match manifest")
		}
		identity := fmt.Sprintf("%s\x00%s\x00%d", record.RecordType, record.RecordID, record.RecordRevision)
		if _, exists := identities[identity]; exists {
			return Bundle{}, fmt.Errorf("duplicate record identity %s/%s/%d", record.RecordType, record.RecordID, record.RecordRevision)
		}
		identities[identity] = struct{}{}
		item, err := EncodeEnvelope(record)
		if err != nil {
			return Bundle{}, fmt.Errorf("encode %s/%s/%d: %w", record.RecordType, record.RecordID, record.RecordRevision, err)
		}
		encoded = append(encoded, item)
		counts[record.RecordType]++
		recordsBytes.Write(item.Bytes)
		recordsBytes.WriteByte('\n')
		setInput.WriteString(item.Envelope.EnvelopeSHA256)
		setInput.WriteByte('\n')
	}
	omissions := append([]Omission{}, spec.Omissions...)
	sort.Slice(omissions, func(i, j int) bool {
		return omissionLess(omissions[i], omissions[j])
	})
	for index, omission := range omissions {
		if omission.RecordFamily == "" || omission.Reason == "" {
			return Bundle{}, fmt.Errorf("omission record family and reason are required")
		}
		if index > 0 && omission == omissions[index-1] {
			return Bundle{}, fmt.Errorf("omissions must be unique")
		}
	}
	manifest := Manifest{
		Format: "llewm-portable-memory", FormatVersion: PortableFormatVersion,
		ExportID: spec.ExportID, LineageID: spec.LineageID, SourceInstanceID: spec.SourceInstanceID,
		CreatedAt: spec.CreatedAt.UTC().Format(time.RFC3339Nano), SourceDatabaseSchema: spec.SourceDatabaseSchema,
		ExportMode: "complete", SelectionProfile: spec.SelectionProfile, RedactionProfile: spec.RedactionProfile,
		RecordCounts: counts, RecordsSHA256: hashHex(recordsBytes.Bytes()), RecordSetSHA256: hashHex(setInput.Bytes()),
		Omissions: omissions,
	}
	manifestBytes, err := canonicalMarshal(manifest)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Manifest: manifest, ManifestBytes: manifestBytes, RecordsBytes: recordsBytes.Bytes(), Records: encoded}, nil
}

// ValidateBundle performs the bounded inspect/validate portion of import without
// writing semantic or catalog state.
func ValidateBundle(manifestBytes, recordsBytes []byte) (Bundle, error) {
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

func validateEnvelope(envelope Envelope, lineageID string) error {
	return validateEnvelopeFormat(envelope, lineageID, "llewm-portable-record", PortableFormatVersion)
}

func validateEnvelopeFormat(envelope Envelope, lineageID, format string, version int) error {
	return validateEnvelopeStructure(envelope, lineageID, format, version, true)
}

func validateEnvelopeStructure(envelope Envelope, lineageID, format string, version int, requireSchema bool) error {
	if envelope.Format != format || envelope.FormatVersion != version || envelope.LineageID != lineageID || !lineagePattern.MatchString(lineageID) {
		return fmt.Errorf("unsupported record format, version, or lineage")
	}
	// Retired types remain valid historical observations during explicit
	// transport conversion, although ordinary current-format imports skip them.
	retiredConversion := version == 2 && RetiredRecordType(envelope.RecordType) && envelope.RecordSchemaVersion == 1
	if requireSchema && !supportedRecordSchema(envelope.RecordType, envelope.RecordSchemaVersion) && !retiredConversion {
		return fmt.Errorf("unsupported record schema %s version %d", envelope.RecordType, envelope.RecordSchemaVersion)
	}
	if envelope.RecordID == "" || envelope.RecordRevision < 0 || envelope.Authority == "" || !isHash(envelope.ContentSHA256) || !isHash(envelope.EnvelopeSHA256) {
		return fmt.Errorf("record has missing or invalid required fields")
	}
	if err := validateCanonicalTime(envelope.RecordedAt); err != nil {
		return fmt.Errorf("recorded time: %w", err)
	}
	payload, err := canonicalObject("payload", envelope.Payload)
	if err != nil {
		return fmt.Errorf("payload validation failed: %w", err)
	}
	if len(payload) > MaxPayloadBytes {
		return fmt.Errorf("payload exceeds %d bytes", MaxPayloadBytes)
	}
	if envelope.ContentSHA256 != hashHex(payload) {
		return fmt.Errorf("payload hash mismatch")
	}
	if len(envelope.Index) > 0 {
		index, err := canonicalObject("index", envelope.Index)
		if err != nil {
			return fmt.Errorf("index validation failed: %w", err)
		}
		if len(index) > MaxIndexBytes {
			return fmt.Errorf("index exceeds %d bytes", MaxIndexBytes)
		}
		if err := validateIndex(index); err != nil {
			return err
		}
	}
	for index, reference := range envelope.Refs {
		if reference.Kind == "" || reference.ID == "" {
			return fmt.Errorf("reference kind and ID are required")
		}
		if index > 0 && !referenceLess(envelope.Refs[index-1], reference) {
			return fmt.Errorf("references are duplicate or out of order")
		}
	}
	want := envelope.EnvelopeSHA256
	envelope.EnvelopeSHA256 = ""
	withoutHash, err := canonicalMarshal(envelope)
	if err != nil || hashHex(withoutHash) != want {
		return fmt.Errorf("envelope hash mismatch")
	}
	return nil
}

func supportedRecordSchema(recordType string, version int) bool {
	latest, supported := supportedRecordSchemas[recordType]
	if !supported {
		return false
	}
	if recordType == "roadmap_node" {
		return version == 1 || version == latest
	}
	return version == latest
}

func validateIndex(input []byte) error {
	var index envelopeIndex
	if err := strictDecode(input, &index); err != nil {
		return fmt.Errorf("index contains unsupported routing metadata: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return err
	}
	if tags, present := raw["tags"]; present && bytes.Equal(tags, []byte("null")) {
		return fmt.Errorf("index tags cannot be null")
	}
	for position, tag := range index.Tags {
		if tag == "" {
			return fmt.Errorf("index tags cannot be empty")
		}
		if position > 0 && !lessUTF16(index.Tags[position-1], tag) {
			return fmt.Errorf("index tags must be sorted and unique")
		}
	}
	return nil
}

func referenceLess(left, right Reference) bool {
	if left.Kind != right.Kind {
		return lessUTF16(left.Kind, right.Kind)
	}
	return lessUTF16(left.ID, right.ID)
}

func omissionLess(left, right Omission) bool {
	if left.RecordFamily != right.RecordFamily {
		return lessUTF16(left.RecordFamily, right.RecordFamily)
	}
	return lessUTF16(left.Reason, right.Reason)
}

func envelopeLess(left, right Envelope) bool {
	if left.RecordType != right.RecordType {
		return lessUTF16(left.RecordType, right.RecordType)
	}
	if left.RecordID != right.RecordID {
		return lessUTF16(left.RecordID, right.RecordID)
	}
	return left.RecordRevision < right.RecordRevision
}

func validateCanonicalTime(value string) error {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return fmt.Errorf("must be canonical UTC RFC3339Nano")
	}
	return nil
}

func equalCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value || value < 1 {
			return false
		}
	}
	return true
}

func strictDecode(input []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func canonicalObject(name string, input []byte) ([]byte, error) {
	value, err := CanonicalizeJSON(input)
	if err != nil {
		return nil, fmt.Errorf("canonicalize %s: %w", name, err)
	}
	if len(value) < 2 || value[0] != '{' || value[len(value)-1] != '}' {
		return nil, fmt.Errorf("%s must be a JSON object", name)
	}
	return value, nil
}

func canonicalMarshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(data)
}

func hashHex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func isHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
