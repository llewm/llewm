package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type indexedDependencies struct {
	Supported bool
	Selectors []portable.ImportDependency
}

var errDependencyRepair = errors.New("dependency selector repair required")

// dependencyDigest seals a complete derived selector set to its immutable
// envelope identity and interpretation version. It detects cache corruption;
// it is not authentication or evidence of semantic import.
func dependencyDigest(hash string, value indexedDependencies) string {
	body, _ := json.Marshal(struct {
		Version      int
		Envelope     string
		Dependencies indexedDependencies
	}{IndexVersion, hash, value})
	return Hash(string(body))
}

// indexedDependencyClosure reuses persistent selectors only for the supplied
// validated snapshot. Unresolved selectors remain indexed without target FKs.
// Every cache set is checked, and missing/damaged sets are decoded and replaced
// atomically. Neither a cached selector nor its digest bypasses the canonical
// importer or the transaction's complete local-state guard.
func (s Index) indexedDependencyClosure(ctx context.Context, objects, changed []portable.EncodedEnvelope) ([]portable.EncodedEnvelope, bool) {
	for _, repair := range []bool{false, true} {
		// Warm reads do not reserve the SQLite writer. A repair restarts with
		// an immediate writer transaction and rechecks the complete snapshot.
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: !repair})
		if err != nil {
			return nil, false
		}
		// Verification and indexed traversal share one SQLite snapshot.
		// New arrivals commonly need cache rows. Detect that before scanning
		// the old snapshot, so the writer retry does not repeat that work.
		if !repair {
			_, err = s.dependencySelectorsWithin(ctx, tx, changed, false, false)
		}
		if err == nil {
			_, err = s.dependencySelectorsWithin(ctx, tx, objects, false, repair)
		}
		if err != nil {
			tx.Rollback()
			if !repair && errors.Is(err, errDependencyRepair) {
				continue
			}
			return nil, false
		}
		result, ok := s.dependencyClosureWithin(ctx, tx, objects, changed)
		if !ok {
			tx.Rollback()
			return nil, false
		}
		if tx.Commit() != nil {
			return nil, false
		}
		return result, true
	}
	return nil, false
}

func (s Index) dependencySelectors(ctx context.Context, objects []portable.EncodedEnvelope) (map[string]indexedDependencies, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := s.dependencySelectorsWithin(ctx, tx, objects, true, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// The traversal needs only a verified persistent index, so it releases each
// batch's selectors after checking its seals rather than retaining every edge.
func (s Index) dependencySelectorsWithin(ctx context.Context, tx *sql.Tx, objects []portable.EncodedEnvelope, collect, repair bool) (map[string]indexedDependencies, error) {
	if len(objects) > portable.MaxRecordCount {
		return nil, fmt.Errorf("dependency records exceed bounds")
	}
	var result map[string]indexedDependencies
	if collect {
		result = make(map[string]indexedDependencies, len(objects))
	}
	seen := make(map[string]bool, len(objects))
	unique := make([]portable.EncodedEnvelope, 0, len(objects))
	total := 0
	for _, object := range objects {
		hash := object.Envelope.EnvelopeSHA256
		if seen[hash] {
			continue
		}
		seen[hash] = true
		total += len(object.Bytes)
		if total > portable.MaxRecordsBytes {
			return nil, fmt.Errorf("dependency bytes exceed bounds")
		}
		unique = append(unique, object)
	}
	// Bounded IN queries avoid one SQLite round trip per historical record and
	// exclude other snapshots, projects and stale interpretation versions.
	count := 0
	selectedEdges := 0
	for start := 0; start < len(unique); start += 128 {
		end := min(start+128, len(unique))
		batch := unique[start:end]
		RecordWork(ctx, "dependency_records_checked", int64(len(batch)))
		args := []any{s.ProjectID, IndexVersion}
		for _, object := range batch {
			args = append(args, object.Envelope.EnvelopeSHA256)
		}
		in := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := tx.QueryContext(ctx, `SELECT envelope_hash,supported,selectors_sha256 FROM history_index_dependency_records WHERE project_id=? AND index_version=? AND envelope_hash IN (`+in+`)`, args...)
		if err != nil {
			return nil, err
		}
		values := make(map[string]indexedDependencies, len(batch))
		seals := make(map[string]string, len(batch))
		for rows.Next() {
			var hash, digest string
			var supported bool
			if err := rows.Scan(&hash, &supported, &digest); err != nil {
				rows.Close()
				return nil, err
			}
			values[hash] = indexedDependencies{Supported: supported}
			seals[hash] = digest
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, `SELECT d.envelope_hash,d.target_type,d.target_id,d.selection_mode,d.target_revision FROM history_index_dependencies d JOIN history_index_dependency_records r ON r.project_id=d.project_id AND r.envelope_hash=d.envelope_hash WHERE d.project_id=? AND r.index_version=? AND d.envelope_hash IN (`+in+`) ORDER BY d.envelope_hash,d.target_type,d.target_id,d.selection_mode,d.target_revision`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var hash string
			var selector portable.ImportDependency
			if err := rows.Scan(&hash, &selector.RecordType, &selector.RecordID, &selector.Mode, &selector.Revision); err != nil {
				rows.Close()
				return nil, err
			}
			count++
			RecordWork(ctx, "dependency_edges_checked", 1)
			if count > MaxScanEntries {
				rows.Close()
				return nil, fmt.Errorf("dependency edges exceed bounds")
			}
			value := values[hash]
			value.Selectors = append(value.Selectors, selector)
			values[hash] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		repairs := make(map[string]indexedDependencies)
		for _, object := range batch {
			hash := object.Envelope.EnvelopeSHA256
			value, exists := values[hash]
			if !exists || seals[hash] != dependencyDigest(hash, value) {
				if !repair {
					return nil, errDependencyRepair
				}
				refs, supported := portable.ImportDependencies(object.Envelope)
				value = indexedDependencies{Supported: supported, Selectors: refs}
				values[hash] = value
				repairs[hash] = value
			}
			if !collect && !value.Supported {
				return nil, fmt.Errorf("unsupported dependency rules")
			}
			if collect {
				result[hash] = value
			}
			selectedEdges += len(value.Selectors)
			if selectedEdges > MaxScanEntries {
				return nil, fmt.Errorf("dependency selectors exceed bounds")
			}
		}
		if len(repairs) > 0 {
			if err := s.saveDependencySelectorsWithin(ctx, tx, repairs); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (s Index) saveDependencySelectors(ctx context.Context, values map[string]indexedDependencies) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.saveDependencySelectorsWithin(ctx, tx, values); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Index) saveDependencySelectorsWithin(ctx context.Context, tx *sql.Tx, values map[string]indexedDependencies) error {
	for hash, value := range values {
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_dependency_records WHERE project_id=? AND envelope_hash=?`, s.ProjectID, hash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_dependency_records(project_id,envelope_hash,index_version,supported,selectors_sha256) VALUES(?,?,?,?,?)`, s.ProjectID, hash, IndexVersion, value.Supported, dependencyDigest(hash, value)); err != nil {
			return err
		}
		if err := insertDependencyEdges(ctx, tx, s.ProjectID, hash, value.Selectors); err != nil {
			return err
		}
	}
	return nil
}

func insertDependencyEdges(ctx context.Context, tx *sql.Tx, project, hash string, selectors []portable.ImportDependency) error {
	for _, selector := range selectors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_dependencies(project_id,envelope_hash,target_type,target_id,selection_mode,target_revision) VALUES(?,?,?,?,?,?)`, project, hash, selector.RecordType, selector.RecordID, selector.Mode, selector.Revision); err != nil {
			return err
		}
	}
	return nil
}
