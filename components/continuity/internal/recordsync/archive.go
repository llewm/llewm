package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Archive owns durable, project-bound transport observations. Its exact bytes
// and provenance are independent of disposable block indexes and status caches.
type Archive struct {
	DB                 *sql.DB
	ProjectID, Lineage string
}

func OpenArchive(ctx context.Context, db *sql.DB, projectID, lineage, legacyDirectory string) (Archive, error) {
	a := Archive{DB: db, ProjectID: projectID, Lineage: lineage}
	if err := openArchiveStore(ctx, a.store(), legacyDirectory); err != nil {
		return Archive{}, err
	}
	return a, nil
}

func (a Archive) store() archiveStore {
	return archiveStore{DB: a.DB, ProjectID: a.ProjectID, Lineage: a.Lineage}
}

func (a Archive) Put(object portable.EncodedEnvelope, origin string) (bool, error) {
	ctx := context.Background()
	added := 0
	err := a.store().transact(ctx, func(tx *sql.Tx) error {
		var err error
		added, err = a.store().putWithin(ctx, tx, "ready", Hash(origin), []portable.EncodedEnvelope{object})
		return err
	})
	return added != 0, err
}

func (a Archive) CaptureLocal(objects []portable.EncodedEnvelope) error {
	ctx := context.Background()
	return a.store().transact(ctx, func(tx *sql.Tx) error {
		return a.captureLocalWithin(ctx, tx, objects)
	})
}

func (a Archive) captureLocalWithin(ctx context.Context, tx *sql.Tx, objects []portable.EncodedEnvelope) error {
	return a.captureLocalVerifiedWithin(ctx, tx, objects, nil)
}

// verified is private to the fresh exporter in CaptureProject. It reuses only
// byte validation, never an archive comparison or a database snapshot receipt.
func (a Archive) captureLocalVerifiedWithin(ctx context.Context, tx *sql.Tx, objects []portable.EncodedEnvelope, verified map[string]portable.EncodedEnvelope) error {
	if _, err := a.store().stampWithin(ctx, tx, "ready"); err != nil {
		return err
	}
	// Before the first possible write, exact unchanged prefixes are no-ops.
	// Stop optimizing at the first uncertain batch: a later write or trigger can
	// change subsequent observations, so the remainder keeps original ordering.
	for len(objects) >= 2 {
		batch := objects[:min(32, len(objects))]
		decodedBatch := make([]portable.EncodedEnvelope, 0, len(batch))
		for _, object := range batch {
			if ctx.Err() != nil {
				break
			}
			decoded, err := captureObservation(ctx, object, verified)
			if err != nil || decoded.Envelope.LineageID != a.Lineage || !bytes.Equal(decoded.Bytes, object.Bytes) {
				break
			}
			decodedBatch = append(decodedBatch, decoded)
		}
		if len(decodedBatch) != len(batch) || !a.store().archiveObjectsMatchWithin(ctx, tx, "", decodedBatch) {
			break
		}
		syncwork.RecordWork(ctx, "capture_match_records", int64(len(batch)))
		objects = objects[len(batch):]
	}
	for _, object := range objects {
		decoded, err := captureObservation(ctx, object, verified)
		if err != nil || decoded.Envelope.LineageID != a.Lineage || !bytes.Equal(decoded.Bytes, object.Bytes) {
			return fmt.Errorf("invalid local archive observation")
		}
		// Any retained origin prevents a later operational export from silently
		// reclassifying a foreign observation as locally originated.
		var origin string
		err = tx.QueryRowContext(ctx, `SELECT origin_hash FROM history_archive_origins WHERE project_id=? AND envelope_hash=? ORDER BY origin_hash LIMIT 1`, a.ProjectID, decoded.Envelope.EnvelopeSHA256).Scan(&origin)
		if errors.Is(err, sql.ErrNoRows) {
			origin = Hash("local")
		} else if err != nil {
			return err
		}
		if _, err := a.store().putValidatedWithin(ctx, tx, "ready", origin, []portable.EncodedEnvelope{decoded}); err != nil {
			return err
		}
	}
	return nil
}

func captureObservation(ctx context.Context, object portable.EncodedEnvelope, verified map[string]portable.EncodedEnvelope) (portable.EncodedEnvelope, error) {
	if saved, ok := verified[object.Envelope.EnvelopeSHA256]; ok && bytes.Equal(saved.Bytes, object.Bytes) {
		syncwork.RecordWork(ctx, "capture_decode_reuses", 1)
		return saved, nil
	}
	syncwork.RecordWork(ctx, "capture_envelope_decodes", 1)
	return portable.DecodeTransportEnvelope(object.Bytes)
}

func (a Archive) Objects(ctx context.Context, endpoint string, relay bool) ([]portable.EncodedEnvelope, error) {
	allowed, _, err := a.objects(ctx, endpoint, relay)
	return allowed, err
}

// objects is the complete recovery/snapshot path. Incremental consumers use
// the archive journal and point reads; they must not call this for every delta.
func (a Archive) objects(ctx context.Context, endpoint string, relay bool) ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
	var allowed, all []portable.EncodedEnvelope
	err := a.store().view(ctx, func(tx *sql.Tx) error {
		if _, err := a.store().stampWithin(ctx, tx, "ready"); err != nil {
			return err
		}
		if _, err := a.store().auditSnapshotWithin(ctx, tx, nil, func(object portable.EncodedEnvelope) {
			all = append(all, object)
		}); err != nil {
			return err
		}
		// This transaction holds the same audited snapshot. Resolve provenance
		// in one range query, retaining the explicit local/source/relay policy.
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT envelope_hash FROM history_archive_origins WHERE project_id=? AND (? OR origin_hash IN (?,?))`, a.ProjectID, relay, Hash("local"), Hash(endpoint))
		if err != nil {
			return err
		}
		permitted := make(map[string]bool)
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				rows.Close()
				return err
			}
			permitted[hash] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, object := range all {
			if permitted[object.Envelope.EnvelopeSHA256] {
				allowed = append(allowed, object)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return allowed, all, nil
}

func (a Archive) ChangeStamp(ctx context.Context) (string, error) {
	var fingerprint string
	err := a.store().view(ctx, func(tx *sql.Tx) error {
		stamp, err := a.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		body, _ := json.Marshal([]any{"archive-state-v1", a.ProjectID, a.Lineage, stamp})
		fingerprint = Hash(string(body))
		return nil
	})
	return fingerprint, err
}

func (a Archive) SaveStatus(endpoint string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > 64<<10 {
		return fmt.Errorf("local endpoint status exceeds bounds")
	}
	_, err = a.DB.Exec(`INSERT INTO history_archive_status(project_id,consumer,value_json,checksum) VALUES(?,?,?,?) ON CONFLICT(project_id,consumer) DO UPDATE SET value_json=excluded.value_json,checksum=excluded.checksum`, a.ProjectID, Hash(endpoint), string(body), Hash(string(body)))
	return err
}

func (a Archive) Status(endpoint string, value any) error {
	var body, checksum string
	if err := a.DB.QueryRow(`SELECT substr(value_json,1,65537),substr(checksum,1,65) FROM history_archive_status WHERE project_id=? AND consumer=?`, a.ProjectID, Hash(endpoint)).Scan(&body, &checksum); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return os.ErrNotExist
		}
		return err
	}
	if len(body) > 64<<10 || Hash(body) != checksum {
		return fmt.Errorf("local endpoint status is damaged")
	}
	return json.Unmarshal([]byte(body), value)
}
