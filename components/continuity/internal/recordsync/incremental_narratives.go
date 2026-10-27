package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

var errNarrativeProofChanged = errors.New("narrative proof changed")

// rememberNarrativeSet records a candidate only if every observation is still
// present under the SQLite writer lock. It is derived, not an import receipt.
func (s Index) rememberNarrativeSet(ctx context.Context, setHash string, objects []portable.EncodedEnvelope) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_narrative_tip SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return err
	}
	if err := s.checkNarrativeLocal(ctx, tx, objects); errors.Is(err, errNarrativeProofChanged) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_valid_sets(project_id,set_hash,index_version,local_state_hash) VALUES(?,?,?,'') ON CONFLICT(project_id,set_hash) DO UPDATE SET index_version=excluded.index_version,local_state_hash=''`, s.ProjectID, setHash, IndexVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_narrative_tip(project_id,set_hash,index_version) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET set_hash=excluded.set_hash,index_version=excluded.index_version`, s.ProjectID, setHash, IndexVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_narrative_members WHERE project_id=?`, s.ProjectID); err != nil {
		return err
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_narrative_members(project_id,envelope_hash) VALUES(?,?)`, s.ProjectID, object.Envelope.EnvelopeSHA256); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s Index) checkNarrativeLocal(ctx context.Context, tx *sql.Tx, objects []portable.EncodedEnvelope) error {
	var recoveries int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_recoveries WHERE project_id=?`, s.ProjectID).Scan(&recoveries); err != nil {
		return err
	}
	if recoveries != 0 {
		return errNarrativeProofChanged
	}
	local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
	if err != nil {
		return err
	}
	for _, object := range objects {
		e := object.Envelope
		held, ok := local[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)]
		if !ok || !bytes.Equal(held.Bytes, object.Bytes) {
			return errNarrativeProofChanged
		}
	}
	_, err = supersessionForkRefs(ctx, tx, s.ProjectID)
	return err
}

// tryNarrativeAdditions takes only an exact validated prior set plus new,
// reference-free narratives. Any uncertainty falls back to the full importer.
func (s Index) tryNarrativeAdditions(ctx context.Context, setHash string, objects []portable.EncodedEnvelope) (ImportResult, bool, error) {
	var prior string
	if err := s.DB.QueryRowContext(ctx, `SELECT set_hash FROM history_index_narrative_tip WHERE project_id=? AND index_version=?`, s.ProjectID, IndexVersion).Scan(&prior); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ImportResult{}, false, nil
		}
		return ImportResult{}, false, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT envelope_hash FROM history_index_narrative_members WHERE project_id=? ORDER BY envelope_hash`, s.ProjectID)
	if err != nil {
		return ImportResult{}, false, err
	}
	members := make(map[string]bool)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return ImportResult{}, false, err
		}
		members[hash] = true
		if len(members) > portable.MaxRecordCount {
			rows.Close()
			return ImportResult{}, false, fmt.Errorf("narrative membership exceeds bounds")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(members) == 0 {
		return ImportResult{}, false, err
	}
	var old, added []portable.EncodedEnvelope
	for _, object := range objects {
		if members[object.Envelope.EnvelopeSHA256] {
			old = append(old, object)
		} else {
			added = append(added, object)
		}
	}
	oldHash, ok := independentNarrativeSet(old)
	if !ok || oldHash != prior || len(old) != len(members) || len(added) == 0 {
		return ImportResult{}, false, nil
	}
	var proof int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_valid_sets WHERE project_id=? AND set_hash=? AND index_version=?`, s.ProjectID, prior, IndexVersion).Scan(&proof); err != nil || proof != 1 {
		return ImportResult{}, false, err
	}
	before, err := supersessionForkRefs(ctx, s.DB, s.ProjectID)
	if err != nil {
		return ImportResult{}, false, nil
	}
	var specs []portable.EnvelopeSpec
	for _, object := range added {
		spec, err := portable.DecodeMemoryEnvelope(object.Bytes)
		if err != nil {
			return ImportResult{}, false, nil
		}
		specs = append(specs, spec)
	}
	update := func(ctx context.Context, tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT set_hash FROM history_index_narrative_tip WHERE project_id=? AND index_version=?`, s.ProjectID, IndexVersion).Scan(&current); err != nil || current != prior {
			return errNarrativeProofChanged
		}
		rows, err := tx.QueryContext(ctx, `SELECT envelope_hash FROM history_index_narrative_members WHERE project_id=?`, s.ProjectID)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				rows.Close()
				return err
			}
			if !members[hash] {
				rows.Close()
				return errNarrativeProofChanged
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != len(members) {
			return errNarrativeProofChanged
		}
		if err := s.checkNarrativeLocal(ctx, tx, objects); err != nil {
			return fmt.Errorf("%w: %v", errNarrativeProofChanged, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_valid_sets(project_id,set_hash,index_version,local_state_hash) VALUES(?,?,?,'') ON CONFLICT(project_id,set_hash) DO UPDATE SET index_version=excluded.index_version,local_state_hash=''`, s.ProjectID, setHash, IndexVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE history_index_narrative_tip SET set_hash=? WHERE project_id=?`, setHash, s.ProjectID); err != nil {
			return err
		}
		for _, object := range added {
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_narrative_members(project_id,envelope_hash) VALUES(?,?)`, s.ProjectID, object.Envelope.EnvelopeSHA256); err != nil {
				return err
			}
		}
		return nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	plan, receipt, err := portable.ImportMemoryRecordsWithin(ctx, s.DB, s.ProjectID, specs, now, update)
	if errors.Is(err, errNarrativeProofChanged) {
		return ImportResult{}, false, nil
	}
	if err != nil {
		return ImportResult{}, true, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	if !plan.ApplyAllowed {
		return ImportResult{}, false, nil
	}
	if receipt == nil {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return ImportResult{}, false, err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `UPDATE history_index_narrative_tip SET index_version=index_version WHERE project_id=?`, s.ProjectID); err != nil {
			return ImportResult{}, false, err
		}
		if err := update(ctx, tx); errors.Is(err, errNarrativeProofChanged) {
			return ImportResult{}, false, nil
		} else if err != nil {
			return ImportResult{}, true, fmt.Errorf("%w: %v", ErrSemanticPending, err)
		}
		if err := tx.Commit(); err != nil {
			return ImportResult{}, true, err
		}
	}
	var result ImportResult
	if receipt != nil {
		result.ImportedRecords = receipt.AppendedRecords
	}
	after, err := supersessionForkRefs(ctx, s.DB, s.ProjectID)
	if err != nil {
		return result, true, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	for ref := range after {
		if !before[ref] {
			result.SupersessionForks++
		}
	}
	return result, true, nil
}
