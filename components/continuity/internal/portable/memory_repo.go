package portable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
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
	// data_version is connection-local. Keep this connection pinned from before
	// the read snapshot until the writer guard; pool reuse cannot prove equality.
	conn, err := db.Conn(ctx)
	if err != nil {
		return MemoryExport{}, err
	}
	defer conn.Close()
	var version int64
	if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&version); err != nil {
		version = 0 // Unknown versions always rebuild under the writer lock.
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MemoryExport{}, err
	}
	defer tx.Rollback()
	snapshot, err := loadMemoryExportSnapshot(ctx, tx, projectID)
	if err != nil {
		return MemoryExport{}, err
	}
	for _, spec := range snapshot.specs {
		if !snapshot.retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] {
			// Release the read transaction before acquiring the writer, avoiding
			// SQLITE_BUSY_SNAPSHOT. Reuse only after a same-connection guard.
			if err := tx.Rollback(); err != nil {
				return MemoryExport{}, err
			}
			return retainMemoryExportSnapshot(ctx, conn, projectID, version, snapshot)
		}
	}
	result, err := snapshot.encode(ctx, tx, projectID)
	if err != nil {
		return MemoryExport{}, err
	}
	return result, tx.Commit()
}

type memoryExportSnapshot struct {
	result    MemoryExport
	specs     []EnvelopeSpec
	validated snapshotEncodings
	retained  map[string]bool
}

func loadMemoryExportSnapshot(ctx context.Context, source queryer, projectID string) (memoryExportSnapshot, error) {
	var snapshot memoryExportSnapshot
	identity, err := identityFrom(ctx, source, projectID)
	if err != nil {
		return snapshot, err
	}
	root, err := projectRootFrom(ctx, source, projectID)
	if err != nil {
		return snapshot, err
	}
	snapshot.result.Identity = identity
	if err := source.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&snapshot.result.DatabaseSchema); err != nil {
		return snapshot, err
	}
	snapshot.validated = make(snapshotEncodings)
	snapshot.specs, _, err = loadOperationalSpecs(ctx, source, projectID, root, identity.LineageID, false, snapshot.validated)
	if err != nil {
		return snapshot, err
	}
	rows, err := source.QueryContext(ctx, `SELECT record_type,record_id,record_revision FROM portable_envelopes WHERE project_id=?`, projectID)
	if err != nil {
		return snapshot, err
	}
	defer rows.Close()
	snapshot.retained = make(map[string]bool)
	for rows.Next() {
		var kind, id string
		var revision int
		if err := rows.Scan(&kind, &id, &revision); err != nil {
			return snapshot, err
		}
		snapshot.retained[RecordKey(kind, id, revision)] = true
	}
	return snapshot, rows.Err()
}

func retainMemoryExportSnapshot(ctx context.Context, conn *sql.Conn, projectID string, version int64, snapshot memoryExportSnapshot) (MemoryExport, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return MemoryExport{}, err
	}
	defer tx.Rollback()
	// Reserve the writer even if the connection defaults to deferred transactions.
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND 0`, projectID); err != nil {
		return MemoryExport{}, err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&current); err != nil || version == 0 || current != version {
		syncwork.RecordWork(ctx, "fallback_export_writer_retry", 1)
		snapshot, err = loadMemoryExportSnapshot(ctx, tx, projectID)
		if err != nil {
			return MemoryExport{}, err
		}
	} else {
		syncwork.RecordWork(ctx, "export_writer_snapshot_reuses", 1)
	}
	result, err := snapshot.encode(ctx, tx, projectID)
	if err != nil {
		return MemoryExport{}, err
	}
	return result, tx.Commit()
}

func (snapshot memoryExportSnapshot) encode(ctx context.Context, tx *sql.Tx, projectID string) (MemoryExport, error) {
	result := snapshot.result
	for _, spec := range snapshot.specs {
		encoded, err := snapshot.validated.encode(ctx, spec)
		if err != nil {
			return MemoryExport{}, fmt.Errorf("encode %s %s: %w", spec.RecordType, spec.RecordID, err)
		}
		// Avoid no-op INSERTs: an unchanged read-only export must not reserve
		// SQLite's sole writer. New identities reach here only with that lock.
		if !snapshot.retained[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] {
			if err := retainEnvelope(ctx, tx, projectID, "export", encoded); err != nil {
				return MemoryExport{}, err
			}
		}
		result.Envelopes = append(result.Envelopes, encoded)
	}
	return result, nil
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
	spec, encoded, err := decodeMemoryEnvelope(line)
	if err != nil {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s: %w", relativePath, err)
	}
	if filepath.ToSlash(MemoryRecordPath(encoded.Envelope)) != filepath.ToSlash(relativePath) {
		return EnvelopeSpec{}, fmt.Errorf("memory record %s is stored at the wrong path", relativePath)
	}
	return spec, nil
}

// decodeMemoryEnvelope retains the encoding produced by full canonical and hash
// validation, so snapshot consumers do not need to encode the same record again.
func decodeMemoryEnvelope(line []byte) (EnvelopeSpec, EncodedEnvelope, error) {
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return EnvelopeSpec{}, EncodedEnvelope{}, err
	}
	recorded, err := parsePortableTime(envelope.RecordedAt)
	if err != nil {
		return EnvelopeSpec{}, EncodedEnvelope{}, err
	}
	spec := EnvelopeSpec{LineageID: envelope.LineageID, RecordType: envelope.RecordType, RecordSchemaVersion: envelope.RecordSchemaVersion, RecordID: envelope.RecordID, RecordRevision: envelope.RecordRevision, RecordedAt: recorded, Authority: envelope.Authority, Index: envelope.Index, Refs: envelope.Refs, Payload: envelope.Payload}
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		return EnvelopeSpec{}, EncodedEnvelope{}, err
	}
	if !bytes.Equal(encoded.Bytes, line) {
		return EnvelopeSpec{}, EncodedEnvelope{}, fmt.Errorf("not a canonical envelope")
	}
	return spec, encoded, nil
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
	spec, _, err := decodeMemoryEnvelope(line)
	return spec, err
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
	return ImportMemoryRecordsWithin(ctx, db, projectID, specs, at, ImportMemoryHooks{})
}

// ImportMemoryHooks runs caller writes atomically after appends, or verifies an
// already-present set against the planner's unchanged local snapshot. Present
// gets a writer lock and fails closed if another connection committed after
// planning. It must not retain the snapshot beyond the callback.
type ImportMemoryHooks struct {
	Appended func(context.Context, *sql.Tx) error
	Present  func(context.Context, *sql.Tx, map[string]EncodedEnvelope) error
}

var ErrImportSnapshotChanged = errors.New("import planning snapshot changed")

func ImportMemoryRecordsWithin(ctx context.Context, db *sql.DB, projectID string, specs []EnvelopeSpec, at time.Time, hooks ImportMemoryHooks) (ImportPlan, *ImportReceipt, error) {
	// SQLite's data_version is meaningful only on the same connection. Keep
	// this connection through planning and writer acquisition; if any other
	// connection commits, apply must replan under the writer transaction.
	conn, err := db.Conn(ctx)
	if err != nil {
		return ImportPlan{}, nil, err
	}
	defer conn.Close()
	identity, err := identityFrom(ctx, conn, projectID)
	if err != nil {
		return ImportPlan{}, nil, err
	}
	var schema string
	if err := conn.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&schema); err != nil {
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
	var dataVersion int64
	versionErr := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&dataVersion)
	var snapshot map[string]EncodedEnvelope
	var capture *map[string]EncodedEnvelope
	if hooks.Present != nil {
		capture = &snapshot
	}
	plan, err := planOperationalImport(ctx, conn, projectID, bundle.ManifestBytes, bundle.RecordsBytes, capture)
	if err != nil || !plan.ApplyAllowed {
		return plan, nil, err
	}
	if plan.Counts[PlanAppend] == 0 {
		if hooks.Present == nil {
			return plan, nil, nil
		}
		if versionErr != nil {
			return plan, nil, ErrImportSnapshotChanged
		}
		return plan, nil, verifyPresentSnapshot(ctx, conn, projectID, dataVersion, snapshot, hooks.Present)
	}
	var previous *reusableImportPlan
	if versionErr == nil {
		previous = newReusableImportPlan(plan, dataVersion, projectID, bundle.ManifestBytes, bundle.RecordsBytes)
	}
	applied, err := applyOperationalImportWithPlan(ctx, conn, projectID, bundle.ManifestBytes, bundle.RecordsBytes, at, hooks.Appended, previous)
	return applied.Plan, applied.Receipt, err
}

func verifyPresentSnapshot(ctx context.Context, conn *sql.Conn, projectID string, version int64, snapshot map[string]EncodedEnvelope, within func(context.Context, *sql.Tx, map[string]EncodedEnvelope) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the writer before checking data_version on this same pinned
	// connection. No canonical writes occur between planning and this guard.
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND 0`, projectID); err != nil {
		return err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&current); err != nil || current != version {
		return ErrImportSnapshotChanged
	}
	if err := within(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

var transportRecordTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

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
	if !transportRecordTypePattern.MatchString(e.RecordType) || e.RecordSchemaVersion < 1 {
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
