package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// ReadBlock uses the disposable inventory only when it reconstructs the exact
// hash-bound bytes. A cache miss is fully decoded by AddBlock; a damaged entry
// is rebuilt from the verified block without discarding endpoint membership.
// Other stale blocks do not block this read; global lookups still refuse them.
func (s Index) ReadBlock(ctx context.Context, root *os.Root, path string) (Block, error) {
	name := filepath.Base(path)
	hash := strings.TrimSuffix(name, ".block")
	expected, err := blockPath(s.Lineage, hash)
	if err != nil || name != hash+".block" || path != expected {
		return Block{}, fmt.Errorf("invalid block path")
	}
	body, err := ReadFile(root, path, MaxBlockBytes)
	if err != nil {
		return Block{}, err
	}
	block := Block{Hash: hash, Bytes: body}
	if err := validateBlockBytes(block); err != nil {
		return Block{}, err
	}
	objects, err := s.blockObjects(ctx, []Block{block})
	if errors.Is(err, ErrIndexRebuild) {
		if repairErr := s.RepairBlock(ctx, block); repairErr != nil {
			return Block{}, repairErr
		}
		objects, err = s.blockObjects(ctx, []Block{block})
	}
	if err != nil {
		return Block{}, err
	}
	block.Objects = objects
	return block, nil
}

func validateBlockBytes(block Block) error {
	if len(block.Bytes) > MaxBlockBytes || len(block.Bytes) <= len(blockHeader) || !bytes.HasPrefix(block.Bytes, []byte(blockHeader)) {
		return fmt.Errorf("invalid history block")
	}
	sum := sha256.Sum256(block.Bytes)
	if block.Hash != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("history block hash mismatch")
	}
	return nil
}

// ImportBlocks uses a prior semantic proof only when its strict replay guards
// hold. All other sets use the complete reference semantic importer.
func (s Index) ImportBlocks(ctx context.Context, blocks []Block) (ImportResult, error) {
	if len(blocks) > portable.MaxRecordCount {
		return ImportResult{}, fmt.Errorf("history block count exceeds bounds")
	}
	seen := make(map[string]bool, len(blocks))
	total := 0
	for _, block := range blocks {
		if err := validateBlockBytes(block); err != nil {
			return ImportResult{}, err
		}
		if seen[block.Hash] {
			return ImportResult{}, fmt.Errorf("duplicate block")
		}
		total += len(block.Bytes) - len(blockHeader)
		if total > portable.MaxRecordsBytes {
			return ImportResult{}, fmt.Errorf("history block records exceed bounds")
		}
		seen[block.Hash] = true
	}
	// Clearing the index here would also discard publication membership for
	// every endpoint. The transport coordinator must first obtain complete
	// inventories for those endpoints and rebuild them together.
	objects, err := s.blockObjects(ctx, blocks)
	if errors.Is(err, ErrIndexRebuild) {
		for _, block := range blocks {
			if repairErr := s.RepairBlock(ctx, block); repairErr != nil {
				return ImportResult{}, repairErr
			}
		}
		objects, err = s.blockObjects(ctx, blocks)
	}
	if err != nil {
		return ImportResult{}, err
	}
	setHash, eligible := independentNarrativeSet(objects)
	if eligible {
		unchanged, err := s.validatedNarrativesPresent(ctx, setHash, objects)
		// Any uncertainty uses the reference importer, which owns semantic
		// error reporting as well as the actual import.
		if err == nil && unchanged {
			return ImportResult{}, nil
		}
		if result, used, err := s.tryNarrativeAdditions(ctx, setHash, objects); used {
			return result, err
		}
	}
	var mixedHash, beforeState string
	if !eligible {
		if candidate, ok := mixedReplaySet(objects); ok {
			mixedHash = candidate
			var seen int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_valid_sets WHERE project_id=? AND set_hash=? AND index_version=?`, s.ProjectID, mixedHash, IndexVersion).Scan(&seen); err == nil && seen != 0 {
				state, proof, snapshotErr := s.mixedReplaySnapshot(ctx, mixedHash, objects, "")
				if snapshotErr == nil {
					if proof {
						return ImportResult{}, nil
					}
					beforeState = state
				}
			}
		}
	}
	result, err := (Importer{DB: s.DB, ProjectID: s.ProjectID, Now: s.Now}).Import(ctx, objects)
	if err == nil && eligible && result.Collisions == 0 && result.Forks == 0 && result.SupersessionForks == 0 && result.DeferredRecords == 0 {
		err = s.rememberNarrativeSet(ctx, setHash, objects)
	} else if err == nil && mixedHash != "" && result.Collisions == 0 && result.Forks == 0 && result.SupersessionForks == 0 && result.DeferredRecords == 0 {
		if beforeState != "" && result.ImportedRecords == 0 {
			// Proof is written only if the complete local state still matches
			// the pre-import snapshot while the writer lock is held.
			_, _, err = s.mixedReplaySnapshot(ctx, mixedHash, objects, beforeState)
		} else {
			// A candidate is not a proof. It avoids an expensive pre-snapshot
			// for a new set, and requires a later unchanged full replay.
			_, err = s.DB.ExecContext(ctx, `INSERT INTO history_index_valid_sets(project_id,set_hash,index_version,local_state_hash) VALUES(?,?,?,'') ON CONFLICT(project_id,set_hash) DO NOTHING`, s.ProjectID, mixedHash, IndexVersion)
		}
	}
	return result, err
}

func mixedReplaySet(objects []portable.EncodedEnvelope) (string, bool) {
	if len(objects) == 0 || len(objects) > portable.MaxRecordCount {
		return "", false
	}
	identities := make(map[string]bool, len(objects))
	var hashes []string
	for _, object := range objects {
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		if !portable.SupportedEnvelope(e) || portable.RetiredRecordType(e.RecordType) || e.RecordType == "redaction_recovery" || identities[key] {
			return "", false
		}
		identities[key] = true
		hashes = append(hashes, e.EnvelopeSHA256)
	}
	sort.Strings(hashes)
	hash := sha256.New()
	for _, value := range hashes {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

// mixedReplaySnapshot takes a writer lock before comparing the complete local
// operational state. A nonempty expected hash requests an atomic proof write;
// mismatch or uncertainty leaves no proof and forces the reference importer.
func (s Index) mixedReplaySnapshot(ctx context.Context, setHash string, objects []portable.EncodedEnvelope, expected string) (string, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_valid_sets SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return "", false, err
	}
	var recoveries int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_recoveries WHERE project_id=?`, s.ProjectID).Scan(&recoveries); err != nil || recoveries != 0 {
		return "", false, err
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
	if err != nil {
		return "", false, err
	}
	for _, object := range objects {
		e := object.Envelope
		held, ok := local[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)]
		if !ok || !bytes.Equal(held.Bytes, object.Bytes) {
			return "", false, nil
		}
	}
	if _, err := supersessionForkRefs(ctx, tx, s.ProjectID); err != nil {
		return "", false, err
	}
	keys := make([]string, 0, len(local))
	for key := range local {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	var length [8]byte
	for _, key := range keys {
		binary.BigEndian.PutUint64(length[:], uint64(len(key)))
		hash.Write(length[:])
		hash.Write([]byte(key))
		sum := sha256.Sum256(local[key].Bytes)
		hash.Write(sum[:])
	}
	state := hex.EncodeToString(hash.Sum(nil))
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

// The first selective case is intentionally narrow. A prior complete import
// proves the exact set passed semantic validation. Narratives with references,
// supersession edges, recoveries, unknown schemas and competing identities
// always take the full path until their invalidation rules are implemented.
func independentNarrativeSet(objects []portable.EncodedEnvelope) (string, bool) {
	if len(objects) == 0 || len(objects) > portable.MaxRecordCount {
		return "", false
	}
	var hashes []string
	identities := make(map[string]bool, len(objects))
	total := 0
	for _, object := range objects {
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		if e.RecordType != "work_narrative" || !portable.SupportedEnvelope(e) || len(e.Refs) != 0 || identities[key] {
			return "", false
		}
		identities[key] = true
		total += len(object.Bytes) + 1
		if total > portable.MaxRecordsBytes {
			return "", false
		}
		hashes = append(hashes, e.EnvelopeSHA256)
	}
	sort.Strings(hashes)
	hash := sha256.New()
	for _, value := range hashes {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

// Local records are compared while the SQLite write transaction prevents a
// concurrent writer from changing the operational state during the decision.
func (s Index) validatedNarrativesPresent(ctx context.Context, setHash string, objects []portable.EncodedEnvelope) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Even with a deferred transaction, this obtains the SQLite writer lock.
	result, err := tx.ExecContext(ctx, `UPDATE history_index_valid_sets SET index_version=index_version WHERE project_id=? AND set_hash=? AND index_version=?`, s.ProjectID, setHash, IndexVersion)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
	if err != nil {
		return false, err
	}
	for _, object := range objects {
		e := object.Envelope
		held, ok := local[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)]
		if !ok || !bytes.Equal(held.Bytes, object.Bytes) {
			return false, nil
		}
	}
	// The full importer queries the current supersession-fork state even for
	// an already-present set. Preserve errors from that query.
	if _, err := supersessionForkRefs(ctx, tx, s.ProjectID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s Index) blockObjects(ctx context.Context, blocks []Block) ([]portable.EncodedEnvelope, error) {
	var objects []portable.EncodedEnvelope
	objectHashes := make(map[string]bool)
	count := 0
	for _, block := range blocks {
		var present int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND block_hash=? AND index_version=?`, s.ProjectID, block.Hash, IndexVersion).Scan(&present); err != nil {
			return nil, err
		}
		if present == 0 {
			// AddBlock validates the complete wire format before recording it.
			if err := s.AddBlock(ctx, block); err != nil {
				return nil, err
			}
		}
		// Reconstruct the exact previously validated block from its inventory.
		// This catches missing, added, or changed rows without revalidating each
		// envelope until the semantic importer needs it. IndexVersion must change
		// whenever block validation rules change.
		rows, err := s.DB.QueryContext(ctx, `SELECT envelope_json,envelope_hash,record_type,record_id,record_revision FROM history_index_records WHERE project_id=? AND block_hash=? ORDER BY envelope_json`, s.ProjectID, block.Hash)
		if err != nil {
			return nil, err
		}
		var bodies [][]byte
		type refKey struct{ hash, kind, id string }
		expectedRefs := make(map[refKey]bool)
		for rows.Next() {
			var body, hash, kind, id string
			var revision int
			if err := rows.Scan(&body, &hash, &kind, &id, &revision); err != nil {
				rows.Close()
				return nil, err
			}
			var envelope portable.Envelope
			if err := json.Unmarshal([]byte(body), &envelope); err != nil || envelope.EnvelopeSHA256 != hash || envelope.RecordType != kind || envelope.RecordID != id || envelope.RecordRevision != revision || envelope.LineageID != s.Lineage {
				rows.Close()
				return nil, fmt.Errorf("%w: inventory identity differs from envelope", ErrIndexRebuild)
			}
			for _, ref := range envelope.Refs {
				expectedRefs[refKey{hash, ref.Kind, ref.ID}] = true
			}
			bodies = append(bodies, []byte(body))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		var reconstructed bytes.Buffer
		reconstructed.WriteString(blockHeader)
		for _, body := range bodies {
			reconstructed.Write(body)
			reconstructed.WriteByte('\n')
		}
		if !bytes.Equal(reconstructed.Bytes(), block.Bytes) {
			return nil, fmt.Errorf("%w: inventory differs from block", ErrIndexRebuild)
		}
		refs, err := s.DB.QueryContext(ctx, `SELECT envelope_hash,ref_kind,ref_id FROM history_index_refs WHERE project_id=? AND block_hash=?`, s.ProjectID, block.Hash)
		if err != nil {
			return nil, err
		}
		for refs.Next() {
			var key refKey
			if err := refs.Scan(&key.hash, &key.kind, &key.id); err != nil {
				refs.Close()
				return nil, err
			}
			if !expectedRefs[key] {
				refs.Close()
				return nil, fmt.Errorf("%w: unexpected inventory reference", ErrIndexRebuild)
			}
			delete(expectedRefs, key)
		}
		err = refs.Err()
		refs.Close()
		if err != nil {
			return nil, err
		}
		if len(expectedRefs) != 0 {
			return nil, fmt.Errorf("%w: missing inventory reference", ErrIndexRebuild)
		}
		for _, body := range bodies {
			count++
			if count > portable.MaxRecordCount {
				return nil, fmt.Errorf("history block records exceed count bound")
			}
			var envelope portable.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil || envelope.LineageID != s.Lineage {
				return nil, fmt.Errorf("%w: invalid cached envelope", ErrIndexRebuild)
			}
			if !objectHashes[envelope.EnvelopeSHA256] {
				objects = append(objects, portable.EncodedEnvelope{Envelope: envelope, Bytes: body})
				objectHashes[envelope.EnvelopeSHA256] = true
			}
		}
	}
	return objects, nil
}
