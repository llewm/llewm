package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// blockObjectsMatchWithin compares bodies, identities and reference sets using
// bounded reads in the caller's transaction, and reports missing memberships in
// input order. It never writes. A mismatch discards every prefetched result so
// sequential repairs can observe their own trigger effects on subsequent rows.
// Read TEXT bodies as BLOBs so byte bounds cannot hide NUL-terminated suffixes.
func (s Index) blockObjectsMatchWithin(ctx context.Context, tx *sql.Tx, block Block) ([]string, bool, error) {
	missing := make(map[string]bool)
	seen := make(map[string]bool, len(block.Objects))
	for _, object := range block.Objects {
		hash := object.Envelope.EnvelopeSHA256
		if seen[hash] {
			return nil, false, nil
		}
		seen[hash] = true
	}
	for start := 0; start < len(block.Objects); start += 32 {
		batch := block.Objects[start:min(start+32, len(block.Objects))]
		expected := make(map[string]portable.EncodedEnvelope, len(batch))
		args := []any{MaxObjectBytes + 1, block.Hash, s.ProjectID}
		refArgs := []any{s.ProjectID}
		for _, object := range batch {
			hash := object.Envelope.EnvelopeSHA256
			expected[hash] = object
			args = append(args, hash)
			refArgs = append(refArgs, hash)
		}
		in := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		syncwork.RecordWork(ctx, "inventory_match_queries", 1)
		rows, err := tx.QueryContext(ctx, `SELECT e.envelope_hash,e.record_type,e.record_id,e.record_revision,substr(CAST(e.envelope_json AS BLOB),1,?),EXISTS(SELECT 1 FROM history_index_block_members m WHERE m.project_id=e.project_id AND m.envelope_hash=e.envelope_hash AND m.block_hash=?) FROM history_index_envelopes e WHERE e.project_id=? AND e.envelope_hash IN (`+in+`)`, args...)
		if err != nil {
			return nil, false, err
		}
		matched, err := func() (bool, error) {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var hash, kind, id string
				var revision int
				var body []byte
				var member bool
				if err := rows.Scan(&hash, &kind, &id, &revision, &body, &member); err != nil {
					return false, err
				}
				syncwork.RecordWork(ctx, "inventory_match_records", 1)
				object, ok := expected[hash]
				e := object.Envelope
				if !ok || kind != e.RecordType || id != e.RecordID || revision != e.RecordRevision || !bytes.Equal(body, object.Bytes) {
					return false, nil
				}
				if !member {
					missing[hash] = true
				}
				count++
			}
			return count == len(batch), rows.Err()
		}()
		if err != nil || !matched {
			return nil, false, err
		}
		refs := make(map[string]map[portable.Reference]bool, len(batch))
		counts := make(map[string]int, len(batch))
		for hash, object := range expected {
			refs[hash] = make(map[portable.Reference]bool, len(object.Envelope.Refs))
			for _, ref := range object.Envelope.Refs {
				refs[hash][ref] = true
			}
		}
		syncwork.RecordWork(ctx, "inventory_match_queries", 1)
		rows, err = tx.QueryContext(ctx, `SELECT envelope_hash,ref_kind,ref_id FROM history_index_refs WHERE project_id=? AND envelope_hash IN (`+in+`)`, refArgs...)
		if err != nil {
			return nil, false, err
		}
		matched, err = func() (bool, error) {
			defer rows.Close()
			for rows.Next() {
				var hash string
				var ref portable.Reference
				if err := rows.Scan(&hash, &ref.Kind, &ref.ID); err != nil {
					return false, err
				}
				if !refs[hash][ref] {
					return false, nil
				}
				counts[hash]++
			}
			if err := rows.Err(); err != nil {
				return false, err
			}
			for hash, wanted := range refs {
				if counts[hash] != len(wanted) {
					return false, nil
				}
			}
			return true, nil
		}()
		if err != nil || !matched {
			return nil, false, err
		}
	}
	var absent []string
	for _, object := range block.Objects {
		hash := object.Envelope.EnvelopeSHA256
		if missing[hash] {
			absent = append(absent, hash)
		}
	}
	return absent, true, nil
}
