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

var errMixedProofChanged = errors.New("mixed replay proof changed")

// tryMixedAdditions handles independent decisions and narratives, and a new
// narrative with its supersession edge to a previously proven narrative.
// All other changes use the full importer.
func (s Index) tryMixedAdditions(ctx context.Context, setHash string, objects []portable.EncodedEnvelope) (ImportResult, bool, error) {
	var prior, priorState string
	err := s.DB.QueryRowContext(ctx, `SELECT t.set_hash,v.local_state_hash FROM history_index_mixed_tip t JOIN history_index_valid_sets v ON v.project_id=t.project_id AND v.set_hash=t.set_hash AND v.index_version=t.index_version WHERE t.project_id=? AND t.index_version=?`, s.ProjectID, IndexVersion).Scan(&prior, &priorState)
	if errors.Is(err, sql.ErrNoRows) || priorState == "" {
		return ImportResult{}, false, nil
	}
	if err != nil {
		return ImportResult{}, false, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT envelope_hash FROM history_index_mixed_members WHERE project_id=?`, s.ProjectID)
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
			return ImportResult{}, false, fmt.Errorf("mixed membership exceeds bounds")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(members) == 0 {
		return ImportResult{}, false, err
	}
	var old, added []portable.EncodedEnvelope
	newIDs := make(map[string]bool)
	excluded := make(map[string]bool)
	oldNarratives := make(map[string]bool)
	newNarratives := make(map[string]bool)
	var edges []portable.EncodedEnvelope
	for _, object := range objects {
		if members[object.Envelope.EnvelopeSHA256] {
			old = append(old, object)
			if object.Envelope.RecordType == "work_narrative" {
				oldNarratives[object.Envelope.RecordID] = true
			}
			continue
		}
		e := object.Envelope
		if e.RecordType == "work_narrative_supersession" {
			edges = append(edges, object)
		} else if (e.RecordType != "decision" && e.RecordType != "work_narrative") || len(e.Refs) != 0 {
			return ImportResult{}, false, nil
		}
		if e.RecordType == "work_narrative" {
			newNarratives[e.RecordID] = true
		}
		added = append(added, object)
		newIDs[e.RecordID] = true
		excluded[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)] = true
	}
	oldHash, eligible := mixedReplaySet(old)
	if !eligible || oldHash != prior || len(old) != len(members) || len(added) == 0 {
		return ImportResult{}, false, nil
	}
	for _, edge := range edges {
		e := edge.Envelope
		if !newNarratives[e.RecordID] || len(e.Refs) != 2 {
			return ImportResult{}, false, nil
		}
		self, parent := false, false
		for _, ref := range e.Refs {
			if ref.Kind != "narrative" {
				return ImportResult{}, false, nil
			}
			self = self || ref.ID == e.RecordID
			parent = parent || (ref.ID != e.RecordID && oldNarratives[ref.ID])
		}
		if !self || !parent {
			return ImportResult{}, false, nil
		}
	}
	for _, object := range old {
		for _, ref := range object.Envelope.Refs {
			if newIDs[ref.ID] {
				return ImportResult{}, false, nil
			}
		}
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
		var current, state string
		if err := tx.QueryRowContext(ctx, `SELECT t.set_hash,v.local_state_hash FROM history_index_mixed_tip t JOIN history_index_valid_sets v ON v.project_id=t.project_id AND v.set_hash=t.set_hash AND v.index_version=t.index_version WHERE t.project_id=? AND t.index_version=?`, s.ProjectID, IndexVersion).Scan(&current, &state); err != nil || current != prior || state != priorState {
			return errMixedProofChanged
		}
		rows, err := tx.QueryContext(ctx, `SELECT envelope_hash FROM history_index_mixed_members WHERE project_id=?`, s.ProjectID)
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
				return errMixedProofChanged
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != len(members) {
			return errMixedProofChanged
		}
		var recoveries int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_recoveries WHERE project_id=?`, s.ProjectID).Scan(&recoveries); err != nil || recoveries != 0 {
			return errMixedProofChanged
		}
		local, err := portable.LocalEnvelopesForImport(ctx, tx, s.ProjectID)
		if err != nil {
			return fmt.Errorf("%w: %v", errMixedProofChanged, err)
		}
		for _, object := range objects {
			e := object.Envelope
			held, ok := local[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)]
			if !ok || !bytes.Equal(held.Bytes, object.Bytes) {
				return errMixedProofChanged
			}
		}
		if localStateHash(local, excluded) != priorState {
			return errMixedProofChanged
		}
		if _, err := supersessionForkRefs(ctx, tx, s.ProjectID); err != nil {
			return fmt.Errorf("%w: %v", errMixedProofChanged, err)
		}
		state = localStateHash(local, nil)
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_valid_sets(project_id,set_hash,index_version,local_state_hash) VALUES(?,?,?,?) ON CONFLICT(project_id,set_hash) DO UPDATE SET index_version=excluded.index_version,local_state_hash=excluded.local_state_hash`, s.ProjectID, setHash, IndexVersion, state); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE history_index_mixed_tip SET set_hash=? WHERE project_id=?`, setHash, s.ProjectID); err != nil {
			return err
		}
		for _, object := range added {
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_mixed_members(project_id,envelope_hash) VALUES(?,?)`, s.ProjectID, object.Envelope.EnvelopeSHA256); err != nil {
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
	if errors.Is(err, errMixedProofChanged) {
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
		if _, err := tx.ExecContext(ctx, `UPDATE history_index_mixed_tip SET index_version=index_version WHERE project_id=?`, s.ProjectID); err != nil {
			return ImportResult{}, false, err
		}
		if err := update(ctx, tx); errors.Is(err, errMixedProofChanged) {
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
