package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

var ErrObservationTracking = errors.New("history observation tracking unavailable")

// ObservationStamp binds a verified transport observation to the archive's
// integrity epoch and the project's derived inventory. Ordinary archive appends
// and canonical changes do not invalidate it; inventory changes and DDL do.
type ObservationStamp struct {
	Inventory    string `json:"inventory"`
	ArchiveEpoch string `json:"archive_epoch"`
	Schema       int64  `json:"schema"`
}

// ObservationMutation carries both snapshots from one reserved writer. A
// consumer may advance only when it still holds the exact Before receipt.
type ObservationMutation struct{ Before, After ObservationStamp }

func (s Index) observationBinding(a Archive) error {
	if s.DB == nil || a.DB != s.DB || a.ProjectID != s.ProjectID || a.Lineage != s.Lineage || s.ProjectID == "" || !lineagePattern.MatchString(s.Lineage) {
		return fmt.Errorf("matching project/archive database required")
	}
	return nil
}

func (s Index) observationStampWithin(ctx context.Context, tx *sql.Tx, a Archive) (ObservationStamp, error) {
	var result ObservationStamp
	stamp, err := a.store().stampWithin(ctx, tx, "ready")
	if err != nil {
		return result, err
	}
	var identity string
	if err := tx.QueryRowContext(ctx, `SELECT lineage_id FROM project_memory_identities WHERE project_id=?`, s.ProjectID).Scan(&identity); err != nil || identity != s.Lineage {
		return result, fmt.Errorf("observation lineage differs from project")
	}
	valid, err := database.HistoryStorageTracking(ctx, tx)
	if err != nil {
		return result, err
	}
	if !valid {
		return result, ErrObservationTracking
	}
	if err := tx.QueryRowContext(ctx, `SELECT token FROM history_index_inventory_generation WHERE project_id=?`, s.ProjectID).Scan(&result.Inventory); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ObservationStamp{}, ErrObservationTracking
		}
		return result, err
	}
	if len(result.Inventory) != 32 || !hex64.MatchString(result.Inventory+result.Inventory) {
		return ObservationStamp{}, ErrObservationTracking
	}
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&result.Schema); err != nil {
		return ObservationStamp{}, err
	}
	body, _ := json.Marshal([]any{"archive-observation-epoch-v1", s.ProjectID, s.Lineage, stamp.Epoch})
	result.ArchiveEpoch = Hash(string(body))
	return result, nil
}

func (s Index) ObservationStamp(ctx context.Context, a Archive) (ObservationStamp, error) {
	var result ObservationStamp
	if err := s.observationBinding(a); err != nil {
		return result, err
	}
	err := a.store().view(ctx, func(tx *sql.Tx) error { var err error; result, err = s.observationStampWithin(ctx, tx, a); return err })
	return result, err
}

// ObservePublicationBatch stages verified block inventory, exact archive bytes
// and destination provenance in one writer transaction. Delta mode returns
// accepted=false without writes if its prior stamp is stale. Complete mode is
// only for a complete verified transport snapshot, and audits durable data
// before committing. Unavailable tracking never produces a reusable stamp.
func (s Index) ObservePublicationBatch(ctx context.Context, a Archive, endpoint string, expected ObservationStamp, complete bool, blocks []Block) (mutation ObservationMutation, accepted bool, err error) {
	if err = s.observationBinding(a); err != nil {
		return
	}
	if endpoint == "" || len(blocks) > portable.MaxRecordCount {
		err = fmt.Errorf("bounded observation endpoint and blocks required")
		return
	}
	verified := make([]Block, 0, len(blocks))
	var auditObjects map[string]portable.EncodedEnvelope
	if complete {
		auditObjects = make(map[string]portable.EncodedEnvelope)
	}
	total, records := 0, 0
	seen := map[string]bool{}
	for _, block := range blocks {
		if seen[block.Hash] {
			err = fmt.Errorf("duplicate observed block")
			return
		}
		seen[block.Hash] = true
		total += len(block.Bytes) - len(blockHeader)
		if total > portable.MaxRecordsBytes {
			err = fmt.Errorf("observed block bytes exceed bounds")
			return
		}
		syncwork.RecordWork(ctx, "block_reads", 1)
		syncwork.RecordWork(ctx, "block_read_bytes", int64(len(block.Bytes)))
		var decoded Block
		decoded, err = DecodeBlock(s.Lineage, block.Hash, block.Bytes)
		if err != nil {
			return
		}
		records += len(decoded.Objects)
		if records > portable.MaxRecordCount {
			err = fmt.Errorf("observed record count exceeds bounds")
			return
		}
		verified = append(verified, decoded)
		if complete {
			for _, object := range decoded.Objects {
				auditObjects[object.Envelope.EnvelopeSHA256] = object
			}
		}
	}
	err = a.store().transact(ctx, func(tx *sql.Tx) error {
		before, trackingErr := s.observationStampWithin(ctx, tx, a)
		if trackingErr != nil && !errors.Is(trackingErr, ErrObservationTracking) {
			return trackingErr
		}
		if trackingErr == nil {
			mutation.Before = before
		}
		if !complete && (trackingErr != nil || before != expected) {
			return nil
		}
		if complete {
			// A missing derived generation is recoverable from this complete source.
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_inventory_generation(project_id,token) VALUES(?,lower(hex(randomblob(16)))) ON CONFLICT DO NOTHING`, s.ProjectID); err != nil {
				return err
			}
		}
		for _, block := range verified {
			if err := s.observeBlockWithin(ctx, tx, a, Hash(endpoint), block); err != nil {
				return err
			}
		}
		if complete {
			if _, err := a.store().auditVerifiedWithin(ctx, tx, auditObjects); err != nil {
				return err
			}
			// Unknown tracking also invalidates semantic cache shortcuts. Canonical
			// observations remain intact; future replay must establish its own proof.
			for _, table := range []string{"history_index_valid_sets", "history_index_narrative_tip", "history_index_mixed_tip"} {
				if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id=?`, s.ProjectID); err != nil {
					return err
				}
			}
		}
		var err error
		mutation.After, err = s.observationStampWithin(ctx, tx, a)
		if errors.Is(err, ErrObservationTracking) {
			mutation.After = ObservationStamp{}
			err = nil
		}
		if err != nil {
			return err
		}
		accepted = true
		return nil
	})
	if err != nil {
		accepted = false
		mutation = ObservationMutation{}
	}
	return
}

func (s Index) materializeBlockWithin(ctx context.Context, tx *sql.Tx, block Block) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_blocks(project_id,block_hash,index_version) VALUES(?,?,?) ON CONFLICT(project_id,block_hash) DO UPDATE SET index_version=excluded.index_version WHERE history_index_blocks.index_version IS NOT excluded.index_version`, s.ProjectID, block.Hash, IndexVersion); err != nil {
		return err
	}
	// Repair only this exact block's membership. Other endpoint publications
	// survive, and valid old rows perform no writes or generation changes.
	args := []any{s.ProjectID, block.Hash}
	for _, object := range block.Objects {
		args = append(args, object.Envelope.EnvelopeSHA256)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(block.Objects)), ",")
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_block_members WHERE project_id=? AND block_hash=? AND envelope_hash NOT IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	if err := s.insertBlockObjects(ctx, tx, block); err != nil {
		return err
	}
	return nil
}

func (s Index) observeBlockWithin(ctx context.Context, tx *sql.Tx, a Archive, endpointHash string, block Block) error {
	if err := s.materializeBlockWithin(ctx, tx, block); err != nil {
		return err
	}

	if _, err := a.store().putValidatedWithin(ctx, tx, "ready", endpointHash, block.Objects); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, endpointHash, block.Hash); err != nil {
		return err
	}
	syncwork.RecordWork(ctx, "git_observation_blocks", 1)
	return nil
}
