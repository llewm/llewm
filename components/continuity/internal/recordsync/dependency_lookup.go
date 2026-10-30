package recordsync

import (
	"context"
	"database/sql"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// dependencyClosureWithin requires complete selector verification in this same
// transaction. The reverse index can then discover earlier dependents without
// building an in-memory graph of unrelated historical selectors. Objects still
// delimit the snapshot: cached owners outside it are never selected.
func (s Index) dependencyClosureWithin(ctx context.Context, tx *sql.Tx, objects, changed []portable.EncodedEnvelope) ([]portable.EncodedEnvelope, bool) {
	// Range reverse selectors match nested suffixes. Query any selectors once,
	// and prior selectors only when a lower target revision expands that suffix.
	lowest := make(map[dependencyIdentity]int)
	visitedEdges := 0
	return traverseDependencies(ctx, objects, changed,
		func(hash string) ([]portable.ImportDependency, bool) {
			rows, err := tx.QueryContext(ctx, `SELECT target_type,target_id,selection_mode,target_revision FROM history_index_dependencies WHERE project_id=? AND envelope_hash=? ORDER BY target_type,target_id,selection_mode,target_revision`, s.ProjectID, hash)
			if err != nil {
				return nil, false
			}
			defer rows.Close()
			var selectors []portable.ImportDependency
			for rows.Next() {
				var selector portable.ImportDependency
				if err := rows.Scan(&selector.RecordType, &selector.RecordID, &selector.Mode, &selector.Revision); err != nil {
					return nil, false
				}
				visitedEdges++
				if visitedEdges > 2*MaxScanEntries {
					return nil, false
				}
				selectors = append(selectors, selector)
			}
			return selectors, rows.Err() == nil
		},
		func(e portable.Envelope) ([]string, bool) {
			key := dependencyIdentity{e.RecordType, e.RecordID}
			prior, seen := lowest[key]
			query := `SELECT envelope_hash FROM history_index_dependencies WHERE project_id=? AND target_type=? AND target_id=? AND selection_mode='exact' AND target_revision=?`
			args := []any{s.ProjectID, e.RecordType, e.RecordID, e.RecordRevision}
			if !seen {
				query += ` UNION ALL SELECT envelope_hash FROM history_index_dependencies WHERE project_id=? AND target_type=? AND target_id=? AND selection_mode='any'`
				args = append(args, s.ProjectID, e.RecordType, e.RecordID)
			}
			if !seen || e.RecordRevision < prior {
				query += ` UNION ALL SELECT envelope_hash FROM history_index_dependencies WHERE project_id=? AND target_type=? AND target_id=? AND selection_mode='prior' AND target_revision>?`
				args = append(args, s.ProjectID, e.RecordType, e.RecordID, e.RecordRevision)
				if seen {
					query += ` AND target_revision<=?`
					args = append(args, prior)
				}
				lowest[key] = e.RecordRevision
			}
			rows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				return nil, false
			}
			defer rows.Close()
			var owners []string
			for rows.Next() {
				var owner string
				if err := rows.Scan(&owner); err != nil {
					return nil, false
				}
				visitedEdges++
				if visitedEdges > 2*MaxScanEntries {
					return nil, false
				}
				owners = append(owners, owner)
			}
			return owners, rows.Err() == nil
		})
}
