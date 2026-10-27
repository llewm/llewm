package recordsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// IndexVersion identifies the derived inventory schema and interpretation.
// Change it whenever block validation, semantic validation, or record and
// reference indexing semantics change.
const IndexVersion = 3

// ErrIndexRebuild means the derived inventory cannot be trusted and must be
// reconstructed from validated transport blocks.
var ErrIndexRebuild = errors.New("history index rebuild required")

// Index is a disposable inventory of validated block contents. It does not
// record whether any envelope was semantically imported.
type Index struct {
	DB        *sql.DB
	ProjectID string
	Lineage   string
	Now       func() time.Time
}

// AddBlock atomically indexes a complete block. A prior version of this block
// requires repair; global dependency lookups still reject any stale entries.
func (s Index) AddBlock(ctx context.Context, block Block) error {
	if block.Hash == "" {
		return fmt.Errorf("indexed block hash is required")
	}
	verified, err := DecodeBlock(s.Lineage, block.Hash, block.Bytes)
	if err != nil {
		return fmt.Errorf("invalid indexed block: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND block_hash=? AND index_version<>?`, s.ProjectID, block.Hash, IndexVersion).Scan(&stale); err != nil {
		return err
	}
	if stale != 0 {
		return fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	if err := s.addBlockWithin(ctx, tx, verified); err != nil {
		return err
	}
	return tx.Commit()
}

// addBlockWithin accepts only a block validated by DecodeBlock and lets the
// caller stage publication intent in the same transaction.
func (s Index) addBlockWithin(ctx context.Context, tx *sql.Tx, verified Block) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, verified.Hash, IndexVersion)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT index_version FROM history_index_blocks WHERE project_id=? AND block_hash=?`, s.ProjectID, verified.Hash).Scan(&version); err != nil {
			return err
		}
		if version != IndexVersion {
			return fmt.Errorf("%w: block version changed", ErrIndexRebuild)
		}
		return nil
	}
	return s.insertBlockObjects(ctx, tx, verified)
}

func (s Index) insertBlockObjects(ctx context.Context, tx *sql.Tx, verified Block) error {
	for _, object := range verified.Objects {
		e := object.Envelope
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_records(project_id,block_hash,envelope_hash,record_type,record_id,record_revision,envelope_json) VALUES(?,?,?,?,?,?,?)`, s.ProjectID, verified.Hash, e.EnvelopeSHA256, e.RecordType, e.RecordID, e.RecordRevision, string(object.Bytes)); err != nil {
			return err
		}
		for _, ref := range e.Refs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_refs(project_id,block_hash,envelope_hash,ref_kind,ref_id) VALUES(?,?,?,?,?)`, s.ProjectID, verified.Hash, e.EnvelopeSHA256, ref.Kind, ref.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RepairBlock reconstructs one damaged inventory entry from exact validated
// block bytes without discarding any endpoint publication memberships. All
// cached semantic-set proofs are invalidated in the same transaction.
func (s Index) RepairBlock(ctx context.Context, block Block) error {
	verified, err := DecodeBlock(s.Lineage, block.Hash, block.Bytes)
	if err != nil || block.Hash == "" {
		return fmt.Errorf("invalid block for index repair")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_refs WHERE project_id=? AND block_hash=?`, s.ProjectID, verified.Hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_records WHERE project_id=? AND block_hash=?`, s.ProjectID, verified.Hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,?,?) ON CONFLICT(project_id,block_hash) DO UPDATE SET index_version=excluded.index_version`, s.ProjectID, verified.Hash, IndexVersion); err != nil {
		return err
	}
	if err := s.insertBlockObjects(ctx, tx, verified); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_valid_sets WHERE project_id=?`, s.ProjectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_narrative_tip WHERE project_id=?`, s.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}

// Clear discards the derived inventory so it can be rebuilt from verified
// blocks. No semantic records or transport files are removed.
func (s Index) Clear(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"history_index_publications", "history_index_refs", "history_index_records", "history_index_blocks", "history_index_valid_sets", "history_index_narrative_tip"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id=?`, s.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rebuild replaces the derived inventory and endpoint membership atomically.
// The caller must supply complete, verified inventories for every endpoint
// whose publication membership must survive. An unavailable or incomplete
// endpoint must stop the coordinator before it calls Rebuild.
func (s Index) Rebuild(ctx context.Context, inventories map[string][]Block) error {
	verified := make(map[string]Block)
	count, size, memberships := 0, 0, 0
	for endpoint, blocks := range inventories {
		if endpoint == "" {
			return fmt.Errorf("rebuild endpoint is required")
		}
		seen := make(map[string]bool)
		for _, block := range blocks {
			memberships++
			if memberships > portable.MaxRecordCount {
				return fmt.Errorf("rebuild memberships exceed bounds")
			}
			if seen[block.Hash] {
				return fmt.Errorf("duplicate endpoint block")
			}
			seen[block.Hash] = true
			value, err := DecodeBlock(s.Lineage, block.Hash, block.Bytes)
			if err != nil || block.Hash == "" {
				return fmt.Errorf("invalid rebuild block")
			}
			if _, ok := verified[value.Hash]; !ok {
				count += len(value.Objects)
				size += len(value.Bytes) - len(blockHeader)
				if count > portable.MaxRecordCount || size > portable.MaxRecordsBytes {
					return fmt.Errorf("rebuild inventory exceeds bounds")
				}
				verified[value.Hash] = value
			}
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"history_index_publications", "history_index_refs", "history_index_records", "history_index_blocks", "history_index_valid_sets", "history_index_narrative_tip"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id=?`, s.ProjectID); err != nil {
			return err
		}
	}
	for _, block := range verified {
		if err := s.addBlockWithin(ctx, tx, block); err != nil {
			return err
		}
	}
	for endpoint, blocks := range inventories {
		for _, block := range blocks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?)`, s.ProjectID, Hash(endpoint), block.Hash); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Versions returns all indexed envelopes for one record revision, including
// competing versions. A block or transport is not treated as the identity.
func (s Index) Versions(ctx context.Context, kind, id string, revision int) ([]portable.EncodedEnvelope, error) {
	return s.lookup(ctx, `SELECT DISTINCT envelope_json FROM history_index_records WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=? ORDER BY envelope_json`, s.ProjectID, kind, id, revision)
}

// Dependents returns indexed envelopes that refer to the supplied identity.
func (s Index) Dependents(ctx context.Context, kind, id string) ([]portable.EncodedEnvelope, error) {
	return s.lookup(ctx, `SELECT DISTINCT r.envelope_json FROM history_index_refs f JOIN history_index_records r ON r.project_id=f.project_id AND r.block_hash=f.block_hash AND r.envelope_hash=f.envelope_hash WHERE f.project_id=? AND f.ref_kind=? AND f.ref_id=? ORDER BY r.envelope_json`, s.ProjectID, kind, id)
}

func (s Index) lookup(ctx context.Context, query string, args ...any) ([]portable.EncodedEnvelope, error) {
	var stale int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND index_version<>?`, s.ProjectID, IndexVersion).Scan(&stale); err != nil {
		return nil, err
	}
	if stale != 0 {
		return nil, fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []portable.EncodedEnvelope
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		object, err := portable.DecodeTransportEnvelope([]byte(body))
		if err != nil || object.Envelope.LineageID != s.Lineage {
			return nil, fmt.Errorf("%w: invalid entry", ErrIndexRebuild)
		}
		result = append(result, object)
	}
	return result, rows.Err()
}
