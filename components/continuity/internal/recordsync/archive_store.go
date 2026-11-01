package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

var errArchiveNotReady = errors.New("durable archive requires verified storage migration")
var errArchiveIntegrity = errors.New("durable archive integrity check failed")

// archiveStore is Archive's durable observation layer, independent of disposable
// history_index_* tables. Existing filesystem observations enter only through
// explicit, verified migration; absence never implies an empty replacement.
type archiveStore struct {
	DB                 *sql.DB
	ProjectID, Lineage string
}

type archiveStamp struct {
	Epoch                                      string
	Sequence, Records, Bytes, Origins, Changes int64
}

type archiveInventory struct {
	Records, Bytes, Origins        int64
	EnvelopesSHA256, OriginsSHA256 string
}

type archiveAudit struct {
	Stamp     archiveStamp
	Inventory archiveInventory
}

type archiveChange struct {
	Sequence                       int64
	Kind, EnvelopeHash, OriginHash string
}

type archiveReceipt struct {
	Project, Lineage, Consumer string
	Stamp                      archiveStamp
}

// initializeWithin creates only staging state, never silently activates an
// empty replacement for an existing archive. Migration must verify its complete
// source inventory and activateWithin must audit the exact staged observations.
func (s archiveStore) initializeWithin(ctx context.Context, tx *sql.Tx) error {
	if s.ProjectID == "" || !lineagePattern.MatchString(s.Lineage) {
		return fmt.Errorf("project and valid lineage required")
	}
	var lineage string
	if err := tx.QueryRowContext(ctx, `SELECT lineage_id FROM project_memory_identities WHERE project_id=?`, s.ProjectID).Scan(&lineage); err != nil {
		return err
	}
	if lineage != s.Lineage {
		return fmt.Errorf("archive lineage differs from project identity")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO history_archive_state(project_id,lineage_id,format_version,status,epoch) VALUES(?,?,1,'staging',lower(hex(randomblob(16)))) ON CONFLICT DO NOTHING`, s.ProjectID, s.Lineage)
	if err != nil {
		return err
	}
	_, err = s.stampWithin(ctx, tx, "staging")
	return err
}

// stampWithin is constant work. SQL triggers maintain the counts and sequence;
// full audit validates those signals independently against the durable bodies.
func (s archiveStore) stampWithin(ctx context.Context, tx *sql.Tx, status string) (archiveStamp, error) {
	var value archiveStamp
	var lineage, actualStatus string
	var version int
	err := tx.QueryRowContext(ctx, `SELECT lineage_id,format_version,status,epoch,last_sequence,record_count,record_bytes,origin_count,change_count FROM history_archive_state WHERE project_id=?`, s.ProjectID).Scan(&lineage, &version, &actualStatus, &value.Epoch, &value.Sequence, &value.Records, &value.Bytes, &value.Origins, &value.Changes)
	if errors.Is(err, sql.ErrNoRows) {
		return value, errArchiveNotReady
	}
	if err != nil {
		return value, err
	}
	if lineage != s.Lineage || version != 1 || (actualStatus != "ready" && actualStatus != "staging") || !validArchiveStamp(value) {
		return value, errArchiveIntegrity
	}
	if status != "" && actualStatus != status {
		return value, errArchiveNotReady
	}
	return value, nil
}

func validArchiveStamp(v archiveStamp) bool {
	return len(v.Epoch) == 32 && hex64.MatchString(v.Epoch+v.Epoch) && v.Sequence >= 0 &&
		v.Records >= 0 && v.Records <= portable.MaxRecordCount && v.Bytes >= 0 && v.Bytes <= portable.MaxRecordsBytes &&
		v.Origins >= 0 && v.Origins <= MaxScanEntries && v.Changes == v.Records+v.Origins &&
		(v.Changes == 0) == (v.Sequence == 0) && v.Sequence >= v.Changes
}

// putWithin accepts only exact validated wire bytes. All fork versions survive
// because the primary key is the envelope hash, not its logical record identity.
// The caller owns the transaction and must roll it back on any error. Staging
// writes are reserved for the explicit migration; normal writes require ready.
func (s archiveStore) putWithin(ctx context.Context, tx *sql.Tx, status, originHash string, objects []portable.EncodedEnvelope) (int, error) {
	if (status != "staging" && status != "ready") || !hex64.MatchString(originHash) || len(objects) > portable.MaxRecordCount {
		return 0, fmt.Errorf("invalid archive batch")
	}
	verified := make([]portable.EncodedEnvelope, 0, len(objects))
	total := 0
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		total += len(object.Bytes) + 1
		if len(object.Bytes) > MaxObjectBytes || total > portable.MaxRecordsBytes {
			return 0, fmt.Errorf("archive batch exceeds bounds")
		}
		decoded, err := portable.DecodeTransportEnvelope(object.Bytes)
		if err != nil || decoded.Envelope.LineageID != s.Lineage || !bytes.Equal(decoded.Bytes, object.Bytes) {
			return 0, fmt.Errorf("invalid archive envelope")
		}
		verified = append(verified, decoded)
	}
	return s.putValidatedWithin(ctx, tx, status, originHash, verified)
}

// putValidatedWithin is private to the ingestion paths which have just decoded
// these exact bytes. Exported parsed fields must never reach it without decode.
func (s archiveStore) putValidatedWithin(ctx context.Context, tx *sql.Tx, status, originHash string, verified []portable.EncodedEnvelope) (int, error) {
	if (status != "staging" && status != "ready") || !hex64.MatchString(originHash) || len(verified) > portable.MaxRecordCount {
		return 0, fmt.Errorf("invalid archive batch")
	}
	if _, err := s.stampWithin(ctx, tx, status); err != nil {
		return 0, err
	}
	statements := statementSet{tx: tx}
	defer statements.close()
	added := 0
	total := 0
	for _, object := range verified {
		if err := ctx.Err(); err != nil {
			return added, err
		}
		total += len(object.Bytes) + 1
		if len(object.Bytes) > MaxObjectBytes || total > portable.MaxRecordsBytes {
			return added, fmt.Errorf("archive batch exceeds bounds")
		}
		e := object.Envelope
		result, err := statements.exec(ctx, `INSERT INTO history_archive_envelopes(project_id,envelope_hash,record_type,record_id,record_revision,initial_origin_hash,envelope_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, e.EnvelopeSHA256, e.RecordType, e.RecordID, e.RecordRevision, originHash, object.Bytes)
		if err != nil {
			return added, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return added, err
		}
		if n == 0 {
			// Never silently repair or overwrite a damaged durable observation.
			stmt, err := statements.prepare(ctx, `SELECT e.record_type,e.record_id,e.record_revision,substr(e.envelope_json,1,?),o.origin_hash FROM history_archive_envelopes e LEFT JOIN history_archive_origins o ON o.project_id=e.project_id AND o.envelope_hash=e.envelope_hash AND o.origin_hash=e.initial_origin_hash WHERE e.project_id=? AND e.envelope_hash=?`)
			if err != nil {
				return added, err
			}
			var kind, id string
			var revision int
			var body []byte
			var origin sql.NullString
			err = stmt.QueryRowContext(ctx, MaxObjectBytes+1, s.ProjectID, e.EnvelopeSHA256).Scan(&kind, &id, &revision, &body, &origin)
			// Exact equality to the already validated input proves its canonical
			// body; still independently check every stored identity and provenance.
			if err != nil || !bytes.Equal(body, object.Bytes) || kind != e.RecordType || id != e.RecordID || revision != e.RecordRevision || !origin.Valid || !hex64.MatchString(origin.String) {
				return added, errArchiveIntegrity
			}
			syncwork.RecordWork(ctx, "archive_verified_bytes_reused", 1)
		}
		originResult, err := statements.exec(ctx, `INSERT INTO history_archive_origins(project_id,envelope_hash,origin_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, e.EnvelopeSHA256, originHash)
		if err != nil {
			return added, err
		}
		originCount, err := originResult.RowsAffected()
		if err != nil {
			return added, err
		}
		syncwork.RecordWork(ctx, "archive_envelopes_inserted", n)
		syncwork.RecordWork(ctx, "archive_origins_inserted", originCount)
		added += int(n)
	}
	return added, nil
}

func (s archiveStore) readObjectWithin(ctx context.Context, tx *sql.Tx, hash string) (portable.EncodedEnvelope, error) {
	if !hex64.MatchString(hash) {
		return portable.EncodedEnvelope{}, fmt.Errorf("invalid archive envelope hash")
	}
	var body []byte
	var kind, id string
	var revision int
	var origin sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT e.record_type,e.record_id,e.record_revision,substr(e.envelope_json,1,?),o.origin_hash FROM history_archive_envelopes e LEFT JOIN history_archive_origins o ON o.project_id=e.project_id AND o.envelope_hash=e.envelope_hash AND o.origin_hash=e.initial_origin_hash WHERE e.project_id=? AND e.envelope_hash=?`, MaxObjectBytes+1, s.ProjectID, hash).Scan(&kind, &id, &revision, &body, &origin); err != nil {
		return portable.EncodedEnvelope{}, err
	}
	if len(body) > MaxObjectBytes || !origin.Valid || !hex64.MatchString(origin.String) {
		return portable.EncodedEnvelope{}, errArchiveIntegrity
	}
	object, err := portable.DecodeTransportEnvelope(body)
	if err != nil || !bytes.Equal(object.Bytes, body) || object.Envelope.LineageID != s.Lineage || object.Envelope.EnvelopeSHA256 != hash || object.Envelope.RecordType != kind || object.Envelope.RecordID != id || object.Envelope.RecordRevision != revision {
		return portable.EncodedEnvelope{}, errArchiveIntegrity
	}
	syncwork.RecordWork(ctx, "archive_envelopes_read", 1)
	return object, nil
}

// authorizedWithin does not turn a retained foreign observation into a local
// one. Explicit relay policy is the only way to publish to an unrelated route.
func (s archiveStore) authorizedWithin(ctx context.Context, tx *sql.Tx, hash, endpointHash string, relay bool) (bool, error) {
	if _, err := s.stampWithin(ctx, tx, "ready"); err != nil {
		return false, err
	}
	if !hex64.MatchString(hash) || !hex64.MatchString(endpointHash) {
		return false, fmt.Errorf("invalid archive route")
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM history_archive_origins WHERE project_id=? AND envelope_hash=? AND (? OR origin_hash IN (?,?)))`, s.ProjectID, hash, relay, Hash("local"), endpointHash).Scan(&allowed)
	return allowed, err
}

// changesWithin returns a snapshot to acknowledge, not a semantic import proof.
// Unknown tracking requires a full audit/materialization rather than a guessed
// delta. Consumer identities are policy hashes; raw endpoints/paths stay out.
func (s archiveStore) changesWithin(ctx context.Context, tx *sql.Tx, consumer string, limit int) (archiveStamp, []archiveChange, string, error) {
	next, err := s.stampWithin(ctx, tx, "ready")
	if err != nil {
		return next, nil, "", err
	}
	if !hex64.MatchString(consumer) || limit < 1 || limit > 4096 {
		return next, nil, "", fmt.Errorf("invalid archive consumer request")
	}
	var body, checksum string
	err = tx.QueryRowContext(ctx, `SELECT substr(snapshot_json,1,2049),substr(checksum,1,65) FROM history_archive_cursors WHERE project_id=? AND consumer=?`, s.ProjectID, consumer).Scan(&body, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return next, nil, "missing_cursor", nil
	}
	if err != nil {
		return next, nil, "", err
	}
	var saved archiveReceipt
	if len(body) > 2048 || Hash(body) != checksum || json.Unmarshal([]byte(body), &saved) != nil || saved.Project != s.ProjectID || saved.Lineage != s.Lineage || saved.Consumer != consumer || !validArchiveStamp(saved.Stamp) || saved.Stamp.Sequence > next.Sequence || saved.Stamp.Changes > next.Changes {
		return next, nil, "damaged_cursor", nil
	}
	if saved.Stamp.Epoch != next.Epoch {
		return next, nil, "archive_changed", nil
	}
	if (saved.Stamp.Sequence == next.Sequence) != (saved.Stamp.Changes == next.Changes) || (saved.Stamp.Sequence == next.Sequence && saved.Stamp != next) {
		return next, nil, "tracking_changed", nil
	}
	if next.Changes-saved.Stamp.Changes > int64(limit) {
		return next, nil, "delta_limit", nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,kind,envelope_hash,COALESCE(origin_hash,'') FROM history_archive_changes WHERE project_id=? AND sequence>? AND sequence<=? ORDER BY sequence LIMIT ?`, s.ProjectID, saved.Stamp.Sequence, next.Sequence, limit+1)
	if err != nil {
		return next, nil, "", err
	}
	defer rows.Close()
	var changes []archiveChange
	for rows.Next() {
		var change archiveChange
		if err := rows.Scan(&change.Sequence, &change.Kind, &change.EnvelopeHash, &change.OriginHash); err != nil {
			return next, nil, "", err
		}
		if !hex64.MatchString(change.EnvelopeHash) || (change.Kind != "envelope" && change.Kind != "origin") || (change.Kind == "origin" && !hex64.MatchString(change.OriginHash)) || (change.Kind == "envelope" && change.OriginHash != "") {
			return next, nil, "damaged_changes", nil
		}
		changes = append(changes, change)
		syncwork.RecordWork(ctx, "archive_journal_rows_read", 1)
	}
	if err := rows.Err(); err != nil {
		return next, nil, "", err
	}
	if int64(len(changes)) != next.Changes-saved.Stamp.Changes || (len(changes) != 0 && changes[len(changes)-1].Sequence != next.Sequence) {
		return next, nil, "missing_changes", nil
	}
	return next, changes, "", nil
}

// rememberWithin must follow successful materialization of this exact snapshot.
// A stale snapshot cannot acknowledge later writes, even on the same connection.
func (s archiveStore) rememberWithin(ctx context.Context, tx *sql.Tx, consumer string, expected archiveStamp) (bool, error) {
	if !hex64.MatchString(consumer) {
		return false, fmt.Errorf("invalid archive consumer")
	}
	current, err := s.stampWithin(ctx, tx, "ready")
	if err != nil || current != expected {
		return false, err
	}
	body, err := json.Marshal(archiveReceipt{Project: s.ProjectID, Lineage: s.Lineage, Consumer: consumer, Stamp: expected})
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_archive_cursors(project_id,consumer,snapshot_json,checksum) VALUES(?,?,?,?) ON CONFLICT(project_id,consumer) DO UPDATE SET snapshot_json=excluded.snapshot_json,checksum=excluded.checksum WHERE history_archive_cursors.snapshot_json IS NOT excluded.snapshot_json OR history_archive_cursors.checksum IS NOT excluded.checksum`, s.ProjectID, consumer, string(body), Hash(string(body)))
	return err == nil, err
}

// auditWithin independently verifies exact durable bodies, all origins, journal
// references and maintained counts. It never uses a disposable block inventory.
func (s archiveStore) auditWithin(ctx context.Context, tx *sql.Tx) (archiveAudit, error) {
	return s.auditVerifiedWithin(ctx, tx, nil)
}

// The optional map contains only freshly decoded private observation inputs.
// Exact durable bytes may reuse that parse; every row, identity, origin and
// maintained count is still independently checked within this transaction.
func (s archiveStore) auditVerifiedWithin(ctx context.Context, tx *sql.Tx, verified map[string]portable.EncodedEnvelope) (archiveAudit, error) {
	return s.auditSnapshotWithin(ctx, tx, verified, nil)
}

// visit receives each completely validated body in hash order. The caller must
// discard collected contents if any later origin, journal or count check fails.
func (s archiveStore) auditSnapshotWithin(ctx context.Context, tx *sql.Tx, verified map[string]portable.EncodedEnvelope, visit func(portable.EncodedEnvelope)) (archiveAudit, error) {
	var audit archiveAudit
	stamp, err := s.stampWithin(ctx, tx, "")
	if err != nil {
		return audit, err
	}
	audit.Stamp = stamp
	objects := sha256.New()
	rows, err := tx.QueryContext(ctx, `SELECT e.envelope_hash,e.record_type,e.record_id,e.record_revision,substr(e.envelope_json,1,?),o.origin_hash FROM history_archive_envelopes e LEFT JOIN history_archive_origins o ON o.project_id=e.project_id AND o.envelope_hash=e.envelope_hash AND o.origin_hash=e.initial_origin_hash WHERE e.project_id=? ORDER BY e.envelope_hash`, MaxObjectBytes+1, s.ProjectID)
	if err != nil {
		return audit, err
	}
	for rows.Next() {
		var hash, kind, id string
		var revision int
		var body []byte
		var origin sql.NullString
		if err := rows.Scan(&hash, &kind, &id, &revision, &body, &origin); err != nil {
			rows.Close()
			return audit, err
		}
		audit.Inventory.Records++
		audit.Inventory.Bytes += int64(len(body) + 1)
		if audit.Inventory.Records > portable.MaxRecordCount || audit.Inventory.Bytes > portable.MaxRecordsBytes || len(body) > MaxObjectBytes || !origin.Valid || !hex64.MatchString(origin.String) {
			rows.Close()
			return audit, errArchiveIntegrity
		}
		object, reused := verified[hash]
		var err error
		if !reused {
			object, err = portable.DecodeTransportEnvelope(body)
			syncwork.RecordWork(ctx, "archive_audit_decodes", 1)
		} else {
			syncwork.RecordWork(ctx, "archive_audit_verified_bytes_reused", 1)
		}
		if err != nil || !bytes.Equal(object.Bytes, body) || object.Envelope.LineageID != s.Lineage || object.Envelope.EnvelopeSHA256 != hash || object.Envelope.RecordType != kind || object.Envelope.RecordID != id || object.Envelope.RecordRevision != revision {
			rows.Close()
			return audit, errArchiveIntegrity
		}
		if visit != nil {
			visit(object)
		}
		objects.Write([]byte(hash + "\x00"))
		syncwork.RecordWork(ctx, "archive_audit_envelopes", 1)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return audit, err
	}
	audit.Inventory.EnvelopesSHA256 = hex.EncodeToString(objects.Sum(nil))
	origins := sha256.New()
	rows, err = tx.QueryContext(ctx, `SELECT o.envelope_hash,o.origin_hash,e.envelope_hash FROM history_archive_origins o LEFT JOIN history_archive_envelopes e ON e.project_id=o.project_id AND e.envelope_hash=o.envelope_hash WHERE o.project_id=? ORDER BY o.envelope_hash,o.origin_hash`, s.ProjectID)
	if err != nil {
		return audit, err
	}
	for rows.Next() {
		var hash, origin string
		var present sql.NullString
		if err := rows.Scan(&hash, &origin, &present); err != nil {
			rows.Close()
			return audit, err
		}
		audit.Inventory.Origins++
		if audit.Inventory.Origins > MaxScanEntries || !present.Valid || !hex64.MatchString(hash) || !hex64.MatchString(origin) {
			rows.Close()
			return audit, errArchiveIntegrity
		}
		origins.Write([]byte(hash + origin + "\x00"))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return audit, err
	}
	audit.Inventory.OriginsSHA256 = hex.EncodeToString(origins.Sum(nil))
	rows, err = tx.QueryContext(ctx, `SELECT c.sequence,c.kind,c.origin_hash,e.envelope_hash,o.origin_hash FROM history_archive_changes c LEFT JOIN history_archive_envelopes e ON e.project_id=c.project_id AND e.envelope_hash=c.envelope_hash LEFT JOIN history_archive_origins o ON o.project_id=c.project_id AND o.envelope_hash=c.envelope_hash AND o.origin_hash=c.origin_hash WHERE c.project_id=? ORDER BY c.sequence`, s.ProjectID)
	if err != nil {
		return audit, err
	}
	var count, last int64
	for rows.Next() {
		var sequence int64
		var kind string
		var origin, envelope, present sql.NullString
		if err := rows.Scan(&sequence, &kind, &origin, &envelope, &present); err != nil {
			rows.Close()
			return audit, err
		}
		count++
		if count > portable.MaxRecordCount+MaxScanEntries || sequence <= last || !envelope.Valid || (kind != "envelope" && kind != "origin") || (kind == "origin" && (!origin.Valid || !present.Valid)) || (kind == "envelope" && origin.Valid) {
			rows.Close()
			return audit, errArchiveIntegrity
		}
		last = sequence
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return audit, err
	}
	if stamp.Records != audit.Inventory.Records || stamp.Bytes != audit.Inventory.Bytes || stamp.Origins != audit.Inventory.Origins || stamp.Changes != count || stamp.Sequence != last {
		return audit, errArchiveIntegrity
	}
	return audit, nil
}

func (s archiveStore) activateWithin(ctx context.Context, tx *sql.Tx, expected archiveInventory) error {
	if _, err := s.stampWithin(ctx, tx, "staging"); err != nil {
		return err
	}
	audit, err := s.auditWithin(ctx, tx)
	if err != nil {
		return err
	}
	if audit.Inventory != expected {
		return fmt.Errorf("archive migration inventory differs from verified source")
	}
	_, err = tx.ExecContext(ctx, `UPDATE history_archive_state SET status='ready',epoch=lower(hex(randomblob(16))) WHERE project_id=? AND status='staging'`, s.ProjectID)
	return err
}
