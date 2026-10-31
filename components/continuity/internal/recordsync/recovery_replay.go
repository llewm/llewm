package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/fork"
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
		if portable.RetiredRecordType(object.Envelope.RecordType) {
			// The reference importer ignores retired observations. Keep them in
			// the exact input fingerprint, but do not require a local record.
			hashes = append(hashes, object.Envelope.EnvelopeSHA256)
			continue
		}
		if !portable.SupportedEnvelope(object.Envelope) {
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
	// Version the proof domain independently of block indexing so older
	// processes cannot reuse proofs made under different recovery rules.
	return "recovery:v4:" + hex.EncodeToString(hash.Sum(nil)), true
}

// recoveryReplaySnapshot proves that all incoming observations, including
// recovery records, resolve to exact existing local envelopes. Proof lookups
// use a consistent read snapshot; proof updates hold the writer lock.
// Uncertainty is a cache miss; the full importer owns recovery semantics.
func (s Index) recoveryReplaySnapshot(ctx context.Context, setHash string, objects []portable.EncodedEnvelope, expected string) (string, bool, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: expected == ""})
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if expected != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE history_index_valid_sets SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
			return "", false, err
		}
	}
	recovered, err := portable.RecoveredEnvelopes(ctx, tx, s.ProjectID)
	if err != nil {
		return "", false, err
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
	if err != nil {
		return "", false, err
	}
	groups := make(map[string][]portable.EncodedEnvelope)
	for _, object := range objects {
		if portable.RetiredRecordType(object.Envelope.RecordType) {
			continue
		}
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		groups[key] = append(groups[key], object)
	}
	normalized := make(map[string]bool, len(groups))
	for _, group := range groups {
		object := group[0]
		if len(group) > 1 {
			// Recovery records are imported together before fork handling. A
			// duplicate correction identity must take the reference path.
			if object.Envelope.RecordType == "redaction_recovery" {
				return "", false, nil
			}
			// Mirror the reference importer's grouping, including recovery
			// normalization and its explicit roadmap equivalence rule.
			heads, err := forkHeads(group, recovered)
			if err != nil || len(heads) == 0 {
				return "", false, nil
			}
			if len(heads) > 1 {
				// A full replay would record each competing head plus the local
				// head. Skip only when both append-only stores already contain
				// every observation it would write.
				first := heads[0]
				held, ok := local[portable.RecordKey(first.RecordType, first.RecordID, first.Revision)]
				if !ok {
					return "", false, nil
				}
				for _, head := range withLocalHead(heads, held) {
					var known int
					err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fork_heads WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=? AND content_sha256=?) AND EXISTS(SELECT 1 FROM fork_head_observations WHERE project_id=? AND envelope_sha256=?)`, s.ProjectID, head.RecordType, head.RecordID, head.Revision, head.ContentSHA256, s.ProjectID, head.EnvelopeSHA256).Scan(&known)
					if err != nil || known == 0 {
						return "", false, err
					}
				}
				continue
			}
			object, err = portable.DecodeTransportEnvelope(heads[0].Envelope)
			if err != nil {
				return "", false, nil
			}
		}
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		// Different raw identities resolving to the same selected identity
		// would be submitted twice to the reference importer.
		if normalized[key] {
			return "", false, nil
		}
		normalized[key] = true
		held, ok := local[key]
		if !ok || (!bytes.Equal(held.Bytes, object.Bytes) && !fork.EquivalentRoadmapNodeEnvelopes([][]byte{held.Bytes, object.Bytes})) {
			return "", false, nil
		}
	}
	if _, err := supersessionForkRefs(ctx, tx, s.ProjectID); err != nil {
		return "", false, err
	}
	state, _ := localStateHashes(ctx, local, recoveryInvocationExclusions(local, objects, recovered), false)
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

// Unreferenced local invocations cannot affect this import. Keep every input
// invocation and every invocation referenced by local or incoming records,
// including transitive invocation parents, in the state fingerprint.
func recoveryInvocationExclusions(local map[string]portable.EncodedEnvelope, objects []portable.EncodedEnvelope, recovered map[string]portable.EncodedEnvelope) map[string]bool {
	needed := make(map[string]bool)
	markRefs := func(object portable.EncodedEnvelope) {
		for _, ref := range object.Envelope.Refs {
			if ref.Kind == "actor_invocation" {
				needed[ref.ID] = true
			}
		}
	}
	for _, object := range objects {
		if object.Envelope.RecordType == "actor_invocation" {
			needed[object.Envelope.RecordID] = true
		}
		markRefs(object)
		if original, ok := recovered[object.Envelope.EnvelopeSHA256]; ok {
			if original.Envelope.RecordType == "actor_invocation" {
				needed[original.Envelope.RecordID] = true
			}
			markRefs(original)
		}
	}
	invocations := make(map[string]portable.EncodedEnvelope)
	for _, object := range local {
		if object.Envelope.RecordType == "actor_invocation" {
			invocations[object.Envelope.RecordID] = object
		} else {
			markRefs(object)
		}
	}
	queue := make([]string, 0, len(needed))
	for id := range needed {
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		for _, ref := range invocations[queue[i]].Envelope.Refs {
			if ref.Kind == "actor_invocation" && !needed[ref.ID] {
				needed[ref.ID] = true
				queue = append(queue, ref.ID)
			}
		}
	}
	excluded := make(map[string]bool)
	for key, object := range local {
		if object.Envelope.RecordType == "actor_invocation" && !needed[object.Envelope.RecordID] {
			excluded[key] = true
		}
	}
	return excluded
}
