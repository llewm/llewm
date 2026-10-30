package recordsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type indexedDependencies struct {
	Supported bool
	Selectors []portable.ImportDependency
}

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
	selectors, err := s.dependencySelectors(ctx, objects)
	if err != nil {
		return nil, false
	}
	return dependencyClosureWithSelectors(ctx, objects, changed, func(e portable.Envelope) ([]portable.ImportDependency, bool) {
		value, ok := selectors[e.EnvelopeSHA256]
		return value.Selectors, ok && value.Supported
	})
}

func (s Index) dependencySelectors(ctx context.Context, objects []portable.EncodedEnvelope) (map[string]indexedDependencies, error) {
	if len(objects) > portable.MaxRecordCount {
		return nil, fmt.Errorf("dependency records exceed bounds")
	}
	result := make(map[string]indexedDependencies, len(objects))
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
		args := []any{s.ProjectID, IndexVersion}
		for _, object := range batch {
			args = append(args, object.Envelope.EnvelopeSHA256)
		}
		in := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		rows, err := s.DB.QueryContext(ctx, `SELECT envelope_hash,supported,selectors_sha256 FROM history_index_dependency_records WHERE project_id=? AND index_version=? AND envelope_hash IN (`+in+`)`, args...)
		if err != nil {
			return nil, err
		}
		seals := make(map[string]string, len(batch))
		for rows.Next() {
			var hash, digest string
			var supported bool
			if err := rows.Scan(&hash, &supported, &digest); err != nil {
				rows.Close()
				return nil, err
			}
			result[hash] = indexedDependencies{Supported: supported}
			seals[hash] = digest
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		rows, err = s.DB.QueryContext(ctx, `SELECT d.envelope_hash,d.target_type,d.target_id,d.selection_mode,d.target_revision FROM history_index_dependencies d JOIN history_index_dependency_records r ON r.project_id=d.project_id AND r.envelope_hash=d.envelope_hash WHERE d.project_id=? AND r.index_version=? AND d.envelope_hash IN (`+in+`) ORDER BY d.envelope_hash,d.target_type,d.target_id,d.selection_mode,d.target_revision`, args...)
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
			if count > MaxScanEntries {
				rows.Close()
				return nil, fmt.Errorf("dependency edges exceed bounds")
			}
			value := result[hash]
			value.Selectors = append(value.Selectors, selector)
			result[hash] = value
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		repairs := make(map[string]indexedDependencies)
		for _, object := range batch {
			hash := object.Envelope.EnvelopeSHA256
			value, exists := result[hash]
			if !exists || seals[hash] != dependencyDigest(hash, value) {
				refs, supported := portable.ImportDependencies(object.Envelope)
				value = indexedDependencies{Supported: supported, Selectors: refs}
				result[hash] = value
				repairs[hash] = value
			}
			selectedEdges += len(value.Selectors)
			if selectedEdges > MaxScanEntries {
				return nil, fmt.Errorf("dependency selectors exceed bounds")
			}
		}
		if len(repairs) > 0 {
			if err := s.saveDependencySelectors(ctx, repairs); err != nil {
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
	return tx.Commit()
}

func insertDependencyEdges(ctx context.Context, tx *sql.Tx, project, hash string, selectors []portable.ImportDependency) error {
	for _, selector := range selectors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_dependencies(project_id,envelope_hash,target_type,target_id,selection_mode,target_revision) VALUES(?,?,?,?,?,?)`, project, hash, selector.RecordType, selector.RecordID, selector.Mode, selector.Revision); err != nil {
			return err
		}
	}
	return nil
}
