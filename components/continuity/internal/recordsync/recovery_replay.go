package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// recoveryReplaySet identifies an exact supported input set containing recovery
// observations. It does not establish that the set is safe to skip.
func recoveryReplaySet(objects []portable.EncodedEnvelope) (string, bool) {
	if len(objects) == 0 || len(objects) > portable.MaxRecordCount {
		return "", false
	}
	var hashes []string
	hasRecovery := false
	for _, object := range objects {
		if !portable.SupportedEnvelope(object.Envelope) || portable.RetiredRecordType(object.Envelope.RecordType) {
			return "", false
		}
		if object.Envelope.RecordType == "redaction_recovery" {
			hasRecovery = true
		}
		hashes = append(hashes, object.Envelope.EnvelopeSHA256)
	}
	if !hasRecovery {
		return "", false
	}
	sort.Strings(hashes)
	hash := sha256.New()
	for _, value := range hashes {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return "recovery:" + hex.EncodeToString(hash.Sum(nil)), true
}

// recoveryReplaySnapshot proves that all incoming observations, including
// recovery records, resolve to exact existing local envelopes. It holds the
// writer lock for the state comparison and any proof update. Uncertainty is a
// cache miss; the full importer remains responsible for recovery semantics.
func (s Index) recoveryReplaySnapshot(ctx context.Context, setHash string, objects []portable.EncodedEnvelope, expected string) (string, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_valid_sets SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return "", false, err
	}
	recovered, err := portable.RecoveredEnvelopes(ctx, tx, s.ProjectID)
	if err != nil {
		return "", false, err
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
	if err != nil {
		return "", false, err
	}
	normalized := make(map[string][]byte, len(objects))
	for _, object := range objects {
		if original, ok := recovered[object.Envelope.EnvelopeSHA256]; ok {
			object = original
		}
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		if previous, ok := normalized[key]; ok && !bytes.Equal(previous, object.Bytes) {
			return "", false, nil
		}
		normalized[key] = object.Bytes
		held, ok := local[key]
		if !ok || !bytes.Equal(held.Bytes, object.Bytes) {
			return "", false, nil
		}
	}
	if _, err := supersessionForkRefs(ctx, tx, s.ProjectID); err != nil {
		return "", false, err
	}
	state := localStateHash(local, nil)
	if expected != "" {
		if state != expected {
			return "", false, nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_valid_sets(project_id,set_hash,index_version,local_state_hash) VALUES(?,?,?,?) ON CONFLICT(project_id,set_hash) DO UPDATE SET index_version=excluded.index_version,local_state_hash=excluded.local_state_hash`, s.ProjectID, setHash, IndexVersion, state); err != nil {
			return "", false, err
		}
	} else {
		var saved string
		err := tx.QueryRowContext(ctx, `SELECT local_state_hash FROM history_index_valid_sets WHERE project_id=? AND set_hash=? AND index_version=?`, s.ProjectID, setHash, IndexVersion).Scan(&saved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if err == nil && saved != "" && saved == state {
			if err := tx.Commit(); err != nil {
				return "", false, err
			}
			return state, true, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return state, false, nil
}
