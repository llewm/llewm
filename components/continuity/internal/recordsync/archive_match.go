package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// archiveObjectsMatchWithin can avoid per-record SQL preparation only when the
// entire input already has exact durable bytes, identities and both provenance
// edges. It performs no writes or repairs. Any uncertainty uses the original
// sequential reader/writer from the beginning, preserving trigger ordering when
// an input actually needs a write. The caller owns the reserved SQLite writer.
func (s archiveStore) archiveObjectsMatchWithin(ctx context.Context, tx *sql.Tx, origin string, objects []portable.EncodedEnvelope) bool {
	if len(objects) == 0 {
		return false
	}
	seen := make(map[string]bool, len(objects))
	total := 0
	for _, object := range objects {
		hash := object.Envelope.EnvelopeSHA256
		total += len(object.Bytes) + 1
		if ctx.Err() != nil || len(object.Bytes) > MaxObjectBytes || total > portable.MaxRecordsBytes || seen[hash] {
			return false
		}
		seen[hash] = true
	}
	// Unknown views or TEMP shadows can make repeated reads nondeterministic.
	// Refusing the optimization leaves their original read/error behavior intact.
	known, err := database.HistoryAtomicSnapshotReaders(ctx, tx)
	if err != nil || !known {
		return false
	}
	for start := 0; start < len(objects); start += 32 {
		batch := objects[start:min(start+32, len(objects))]
		expected := make(map[string]portable.EncodedEnvelope, len(batch))
		args := []any{MaxObjectBytes + 1, origin, s.ProjectID}
		for _, object := range batch {
			hash := object.Envelope.EnvelopeSHA256
			expected[hash] = object
			args = append(args, hash)
		}
		syncwork.RecordWork(ctx, "archive_match_queries", 1)
		rows, err := tx.QueryContext(ctx, `SELECT e.envelope_hash,e.record_type,e.record_id,e.record_revision,substr(e.envelope_json,1,?),o.origin_hash,EXISTS(SELECT 1 FROM history_archive_origins p WHERE p.project_id=e.project_id AND p.envelope_hash=e.envelope_hash AND p.origin_hash=?) FROM history_archive_envelopes e LEFT JOIN history_archive_origins o ON o.project_id=e.project_id AND o.envelope_hash=e.envelope_hash AND o.origin_hash=e.initial_origin_hash WHERE e.project_id=? AND e.envelope_hash IN (`+strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")+`)`, args...)
		if err != nil {
			return false
		}
		matched := func() bool {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var hash, kind, id string
				var revision int
				var body []byte
				var initial sql.NullString
				var held bool
				if err := rows.Scan(&hash, &kind, &id, &revision, &body, &initial, &held); err != nil {
					return false
				}
				syncwork.RecordWork(ctx, "archive_match_records", 1)
				object, ok := expected[hash]
				e := object.Envelope
				if !ok || kind != e.RecordType || id != e.RecordID || revision != e.RecordRevision || !bytes.Equal(body, object.Bytes) || !initial.Valid || !hex64.MatchString(initial.String) || !held {
					return false
				}
				count++
			}
			return count == len(batch) && rows.Err() == nil && rows.Close() == nil
		}()
		if !matched {
			return false
		}
	}
	return true
}
