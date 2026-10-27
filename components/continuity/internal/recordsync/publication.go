package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// ObservePublication records destination provenance before indexing a received
// block. Replaying verified destination blocks also rebuilds a lost catalog.
func (s Index) ObservePublication(ctx context.Context, archive Archive, endpoint string, block Block) error {
	if endpoint == "" || archive.Lineage != s.Lineage {
		return fmt.Errorf("publication endpoint and matching lineage are required")
	}
	verified, err := DecodeBlock(s.Lineage, block.Hash, block.Bytes)
	if err != nil || block.Hash == "" {
		return fmt.Errorf("invalid observed block")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return err
	}
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND index_version<>?`, s.ProjectID, IndexVersion).Scan(&stale); err != nil {
		return err
	}
	if stale != 0 {
		return fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	for _, object := range verified.Objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := archive.Put(object, endpoint); err != nil {
			return err
		}
	}
	if err := s.addBlockWithin(ctx, tx, verified); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, Hash(endpoint), verified.Hash); err != nil {
		return err
	}
	return tx.Commit()
}

// PlanPublication stages immutable publication intent before returning blocks
// for delivery. Callers publish every returned block: this also repairs a
// block missing from the destination after an interrupted external sync.
// The index is derived; after clearing it, rebuild publication intent from
// verified destination blocks before planning additions. An incomplete or
// invalid destination scan must be retried before rebuilding its catalog.
func (s Index) PlanPublication(ctx context.Context, archive Archive, endpoint string, relay bool) ([]Block, error) {
	if endpoint == "" || archive.Lineage != s.Lineage {
		return nil, fmt.Errorf("publication endpoint and matching lineage are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer lock before scanning the archive, so plans with the
	// same authorization do not stage from stale, out-of-order snapshots.
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return nil, err
	}
	allowed, err := archive.Objects(ctx, endpoint, relay)
	if err != nil {
		return nil, err
	}
	byHash := make(map[string]portable.EncodedEnvelope, len(allowed))
	for _, object := range allowed {
		byHash[object.Envelope.EnvelopeSHA256] = object
	}
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND index_version<>?`, s.ProjectID, IndexVersion).Scan(&stale); err != nil {
		return nil, err
	}
	if stale != 0 {
		return nil, fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	endpointHash := Hash(endpoint)
	rows, err := tx.QueryContext(ctx, `SELECT p.block_hash,r.envelope_hash,r.envelope_json FROM history_index_publications p LEFT JOIN history_index_records r ON r.project_id=p.project_id AND r.block_hash=p.block_hash WHERE p.project_id=? AND p.endpoint_hash=? ORDER BY p.block_hash,r.envelope_json`, s.ProjectID, endpointHash)
	if err != nil {
		return nil, err
	}
	type saved struct {
		hash  string
		items []struct{ hash, body string }
	}
	var savedBlocks []saved
	rowCount, rowBytes := 0, 0
	for rows.Next() {
		rowCount++
		if rowCount > portable.MaxRecordCount {
			rows.Close()
			return nil, fmt.Errorf("%w: publication record count exceeds bounds", ErrIndexRebuild)
		}
		var hash string
		var objectHash, body sql.NullString
		if err := rows.Scan(&hash, &objectHash, &body); err != nil {
			rows.Close()
			return nil, err
		}
		if len(savedBlocks) == 0 || savedBlocks[len(savedBlocks)-1].hash != hash {
			savedBlocks = append(savedBlocks, saved{hash: hash})
			if len(savedBlocks) > portable.MaxRecordCount {
				rows.Close()
				return nil, fmt.Errorf("%w: too many publication blocks", ErrIndexRebuild)
			}
		}
		if !body.Valid || !objectHash.Valid {
			rows.Close()
			return nil, fmt.Errorf("%w: publication has no records", ErrIndexRebuild)
		}
		rowBytes += len(body.String) + 1
		if rowBytes > portable.MaxRecordsBytes {
			rows.Close()
			return nil, fmt.Errorf("%w: publication records exceed bounds", ErrIndexRebuild)
		}
		savedBlocks[len(savedBlocks)-1].items = append(savedBlocks[len(savedBlocks)-1].items, struct{ hash, body string }{objectHash.String, body.String})
		if len(savedBlocks[len(savedBlocks)-1].items) > MaxBlockRecords || len(body.String) > MaxObjectBytes {
			rows.Close()
			return nil, fmt.Errorf("%w: publication exceeds bounds", ErrIndexRebuild)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool, len(allowed))
	var result []Block
	for _, saved := range savedBlocks {
		var body bytes.Buffer
		body.WriteString(blockHeader)
		permitted, addsRecord := true, false
		var objects []portable.EncodedEnvelope
		for _, item := range saved.items {
			if body.Len()+len(item.body)+1 > MaxBlockBytes {
				return nil, fmt.Errorf("%w: publication exceeds bounds", ErrIndexRebuild)
			}
			body.WriteString(item.body)
			body.WriteByte('\n')
			var envelope portable.Envelope
			if err := json.Unmarshal([]byte(item.body), &envelope); err != nil || envelope.EnvelopeSHA256 != item.hash || envelope.LineageID != s.Lineage {
				return nil, fmt.Errorf("%w: invalid publication entry", ErrIndexRebuild)
			}
			existing, ok := byHash[item.hash]
			if !ok || !bytes.Equal(existing.Bytes, []byte(item.body)) {
				permitted = false
				continue
			}
			objects = append(objects, existing)
			if !covered[item.hash] {
				addsRecord = true
			}
		}
		sum := sha256.Sum256(body.Bytes())
		if len(saved.items) == 0 || len(saved.items) > MaxBlockRecords || body.Len() > MaxBlockBytes || hex.EncodeToString(sum[:]) != saved.hash {
			return nil, fmt.Errorf("%w: invalid publication block", ErrIndexRebuild)
		}
		if permitted && addsRecord {
			block := Block{Hash: saved.hash, Bytes: append([]byte(nil), body.Bytes()...), Objects: objects}
			result = append(result, block)
			for _, object := range block.Objects {
				covered[object.Envelope.EnvelopeSHA256] = true
			}
		}
	}
	var missing []portable.EncodedEnvelope
	for _, object := range allowed {
		if !covered[object.Envelope.EnvelopeSHA256] {
			missing = append(missing, object)
		}
	}
	newBlocks, err := packBlocks(s.Lineage, missing)
	if err != nil {
		return nil, err
	}
	for _, block := range newBlocks {
		if err := s.addBlockWithin(ctx, tx, block); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, endpointHash, block.Hash); err != nil {
			return nil, err
		}
		result = append(result, block)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
