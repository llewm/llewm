package portable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
)

// The memory repository stores each immutable envelope once, at a path derived
// from its lineage, identity, and envelope hash. Writers only add files, so Git
// never conflicts; two versions of one identity land side by side, and import
// keeps them as a revision fork (or reports a collision for an unrevisioned
// record).

// MemoryRepositoryRoot is the top-level directory of the memory repository
// format; it names PortableFormatVersion.
const MemoryRepositoryRoot = "v3"

// FormerMemoryRepositoryRoot holds format-2 records, which ordinary sync cannot
// read. Sync refuses a clone that still has them for its lineage until
// `llewm sync convert` removes them.
const FormerMemoryRepositoryRoot = "v2"

// FormerMemoryLineageDir returns the lineage's format-2 directory, relative
// to the repository.
func FormerMemoryLineageDir(lineageID string) string {
	return filepath.Join(FormerMemoryRepositoryRoot, strings.TrimPrefix(lineageID, "lineage:"))
}

// MemoryLineageDir returns the lineage partition, relative to the repository.
func MemoryLineageDir(lineageID string) string {
	return filepath.Join(MemoryRepositoryRoot, strings.TrimPrefix(lineageID, "lineage:"), "records")
}

// MemoryRecordPath returns the repository-relative path of one envelope.
func MemoryRecordPath(envelope Envelope) string {
	identity := envelope.RecordType + "\x00" + envelope.RecordID + "\x00" + strconv.Itoa(envelope.RecordRevision)
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(MemoryLineageDir(envelope.LineageID), envelope.RecordType, hex.EncodeToString(sum[:]), envelope.EnvelopeSHA256+".json")
}

// MemoryExport is the project's current curated records, encoded for the
// memory repository.
type MemoryExport struct {
	Identity       Identity
	DatabaseSchema string
	Envelopes      []EncodedEnvelope
}

// EncodeMemoryExport encodes every curated record of the project.
func EncodeMemoryExport(ctx context.Context, db *sql.DB, projectID string) (MemoryExport, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryExport{}, err
	}
	defer tx.Rollback()
	identity, err := identityFrom(ctx, tx, projectID)
	if err != nil {
		return MemoryExport{}, err
	}
	root, err := projectRootFrom(ctx, tx, projectID)
	if err != nil {
		return MemoryExport{}, err
	}
	result := MemoryExport{Identity: identity}
	if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&result.DatabaseSchema); err != nil {
		return MemoryExport{}, err
	}
	specs, _, err := loadOperationalSpecs(ctx, tx, projectID, root, identity.LineageID, false)
	if err != nil {
		return MemoryExport{}, err
	}
	for _, spec := range specs {
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return MemoryExport{}, fmt.Errorf("encode %s %s: %w", spec.RecordType, spec.RecordID, err)
		}
		if err := retainEnvelope(ctx, tx, projectID, "export", encoded); err != nil {
			return MemoryExport{}, err
		}
		result.Envelopes = append(result.Envelopes, encoded)
	}
	return result, tx.Commit()
}

// MemoryFileBytes is the on-disk form of one envelope: its canonical line.
func MemoryFileBytes(encoded EncodedEnvelope) []byte {
	return append(append([]byte(nil), encoded.Bytes...), '\n')
}

// DecodeMemoryFile parses one memory-repository file and proves it is the
// canonical encoding of the envelope it claims to be, stored at its own path.
func DecodeMemoryFile(relativePath string, body []byte) (EnvelopeSpec, error) {
	line, err := oneReviewedEnvelope(relativePath, body)
	if err != nil {
		return EnvelopeSpec{}, err
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s: %w", relativePath, err)
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s: %w", relativePath, err)
	}
	spec := EnvelopeSpec{LineageID: envelope.LineageID, RecordType: envelope.RecordType, RecordSchemaVersion: envelope.RecordSchemaVersion, RecordID: envelope.RecordID, RecordRevision: envelope.RecordRevision, RecordedAt: recorded, Authority: envelope.Authority, Index: envelope.Index, Refs: envelope.Refs, Payload: envelope.Payload}
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s: %w", relativePath, err)
	}
	if !bytes.Equal(encoded.Bytes, line) {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s is not a canonical envelope", relativePath)
	}
	if filepath.ToSlash(MemoryRecordPath(encoded.Envelope)) != filepath.ToSlash(relativePath) {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s is stored at the wrong path", relativePath)
	}
	return spec, nil
}

// DecodeStoredMemoryEnvelope reads immutable historical fork observations.
// Old observations remain unchanged in SQLite; only this explicit historical
// read re-encodes format 2. External memory imports still require format 3.
func DecodeStoredMemoryEnvelope(line []byte) (EnvelopeSpec, error) {
	var envelope Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return EnvelopeSpec{}, fmt.Errorf("decode envelope: %w", err)
	}
	if envelope.Format == "llmem-portable-record" && envelope.FormatVersion == 2 {
		converted, err := ConvertPreviousEnvelope(line)
		if err != nil {
			return EnvelopeSpec{}, err
		}
		line, envelope = converted.Bytes, converted.Envelope
	}
	return DecodeMemoryEnvelope(line)
}

// DecodeMemoryEnvelope is the current-format network/import reader.
func DecodeMemoryEnvelope(line []byte) (EnvelopeSpec, error) {
	var envelope Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return EnvelopeSpec{}, err
	}
	return DecodeMemoryFile(MemoryRecordPath(envelope), append(append([]byte(nil), line...), '\n'))
}

// LocalEnvelopes returns the project's own encoded records keyed by
// RecordKey.
func LocalEnvelopes(ctx context.Context, db *sql.DB, projectID string) (map[string]EncodedEnvelope, error) {
	loaded, err := LoadOperationalRecords(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]EncodedEnvelope, len(loaded.Records))
	for _, spec := range loaded.Records {
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return nil, err
		}
		result[recordKey(encoded.Envelope)] = encoded
	}
	return result, nil
}

// RecordKey identifies one record revision in LocalEnvelopes.
func RecordKey(recordType, recordID string, revision int) string {
	return recordKey(Envelope{RecordType: recordType, RecordID: recordID, RecordRevision: revision})
}

// RecordVersions returns the project's own encoded revisions of one record,
// lowest revision first.
func RecordVersions(ctx context.Context, db *sql.DB, projectID, recordType, recordID string) ([]EncodedEnvelope, error) {
	local, err := LocalEnvelopes(ctx, db, projectID)
	if err != nil {
		return nil, err
	}
	var result []EncodedEnvelope
	for _, encoded := range local {
		if encoded.Envelope.RecordType == recordType && encoded.Envelope.RecordID == recordID {
			result = append(result, encoded)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Envelope.RecordRevision < result[j].Envelope.RecordRevision })
	return result, nil
}

// ImportMemoryRecords plans the given records as one bundle for the project and
// applies it when the plan allows and something is new. The bundle is attributed
// to the importing instance because records may come from many instances.
func ImportMemoryRecords(ctx context.Context, db *sql.DB, projectID string, specs []EnvelopeSpec, at time.Time) (ImportPlan, *ImportReceipt, error) {
	return ImportMemoryRecordsWithin(ctx, db, projectID, specs, at, nil)
}

// ImportMemoryRecordsWithin is ImportMemoryRecords with the caller's own writes
// (within) in the import transaction. within runs only when records are
// appended.
func ImportMemoryRecordsWithin(ctx context.Context, db *sql.DB, projectID string, specs []EnvelopeSpec, at time.Time, within func(context.Context, *sql.Tx) error) (ImportPlan, *ImportReceipt, error) {
	identity, err := identityFrom(ctx, db, projectID)
	if err != nil {
		return ImportPlan{}, nil, err
	}
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&schema); err != nil {
		return ImportPlan{}, nil, err
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	bundle, err := BuildBundle(BundleSpec{
		ExportID: "sync:" + activity.NewID(), LineageID: identity.LineageID, SourceInstanceID: identity.InstanceID,
		CreatedAt: at, SourceDatabaseSchema: schema, SelectionProfile: "curated-v1",
		RedactionProfile: DefaultRedactionProfile(), Omissions: curatedV1Omissions,
	}, specs)
	if err != nil {
		return ImportPlan{}, nil, err
	}
	plan, err := PlanOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes)
	if err != nil || !plan.ApplyAllowed || plan.Counts[PlanAppend] == 0 {
		return plan, nil, err
	}
	applied, err := applyOperationalImport(ctx, db, projectID, bundle.ManifestBytes, bundle.RecordsBytes, at, within)
	return applied.Plan, applied.Receipt, err
}

// DecodeTransportEnvelope verifies current wire-format integrity without claiming
// to understand an unknown record schema. Such objects may be retained/relayed
// under explicit endpoint authorization, but ordinary semantic readers remain strict.
func DecodeTransportEnvelope(body []byte) (EncodedEnvelope, error) {
	if len(body) > MaxPayloadBytes+MaxIndexBytes+(64<<10) {
		return EncodedEnvelope{}, fmt.Errorf("transport envelope exceeds bounds")
	}
	line := bytes.TrimSuffix(body, []byte("\n"))
	var e Envelope
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return EncodedEnvelope{}, err
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`).MatchString(e.RecordType) || e.RecordSchemaVersion < 1 {
		return EncodedEnvelope{}, fmt.Errorf("invalid transport record type/schema")
	}
	if err := validateEnvelopeStructure(e, e.LineageID, "llewm-portable-record", PortableFormatVersion, false); err != nil {
		return EncodedEnvelope{}, err
	}
	canonical, err := canonicalMarshal(e)
	if err != nil || !bytes.Equal(canonical, line) {
		return EncodedEnvelope{}, fmt.Errorf("noncanonical transport envelope")
	}
	return EncodedEnvelope{Envelope: e, Bytes: canonical}, nil
}

// SupportedEnvelope distinguishes semantic support from transport integrity.
func SupportedEnvelope(e Envelope) bool {
	return supportedRecordSchema(e.RecordType, e.RecordSchemaVersion)
}
