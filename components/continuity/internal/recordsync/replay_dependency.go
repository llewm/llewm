package recordsync

import (
	"context"
	"database/sql"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

const maxSemanticReplayRecords = 128
const maxSemanticReplayEdges = 4096

type replayObjectSet struct {
	archive Archive
	tx      *sql.Tx
	objects map[string]portable.EncodedEnvelope
	bytes   int
}

func (p *replayObjectSet) get(ctx context.Context, hash string) (portable.EncodedEnvelope, error) {
	if object, exists := p.objects[hash]; exists {
		return object, nil
	}
	if len(p.objects) >= maxSemanticReplayRecords {
		return portable.EncodedEnvelope{}, errReplayJournalFull
	}
	object, err := p.archive.store().readObjectWithin(ctx, p.tx, hash)
	if err != nil {
		return object, err
	}
	p.bytes += len(object.Bytes)
	if p.bytes > portable.MaxRecordsBytes {
		return portable.EncodedEnvelope{}, errReplayJournalFull
	}
	p.objects[hash] = object
	syncwork.RecordWork(ctx, "dependency_membership_records", 1)
	return object, nil
}

// replayDependencyClosureWithin relies on a complete, generation-guarded
// selector baseline and a verified current raw projection. It reads only reached
// archive bodies and typed forward/reverse index ranges. Competing versions are
// included together; the caller decides whether their semantics are eligible.
func (s Index) replayDependencyClosureWithin(ctx context.Context, tx *sql.Tx, objects *replayObjectSet, changed []string, added map[string]bool) ([]portable.EncodedEnvelope, error) {
	var fresh []portable.EncodedEnvelope
	for hash := range added {
		object, err := objects.get(ctx, hash)
		if err != nil {
			return nil, err
		}
		if _, supported := portable.ImportDependencies(object.Envelope); !supported {
			return nil, errReplayJournalFull
		}
		fresh = append(fresh, object)
	}
	// Install every arriving owner's selectors before looking for reverse
	// edges, so traversal order cannot hide a new dependent in this batch.
	if _, err := s.dependencySelectorsWithin(ctx, tx, fresh, false, true); err != nil {
		return nil, err
	}
	visitedRows := 0
	readHashes := func(query string, args ...any) ([]string, error) {
		args = append(args, maxSemanticReplayRecords+1)
		rows, err := tx.QueryContext(ctx, query+` ORDER BY envelope_hash LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var hashes []string
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				return nil, err
			}
			hashes = append(hashes, hash)
			visitedRows++
			syncwork.RecordWork(ctx, "semantic_dependency_rows_read", 1)
			if len(hashes) > maxSemanticReplayRecords || visitedRows > maxSemanticReplayEdges {
				return nil, errReplayJournalFull
			}
		}
		return hashes, rows.Err()
	}
	selected := map[string]bool{}
	var queue []string
	add := func(hashes []string) error {
		for _, hash := range hashes {
			if !selected[hash] {
				selected[hash] = true
				queue = append(queue, hash)
				if len(queue) > maxSemanticReplayRecords {
					return errReplayJournalFull
				}
			}
		}
		return nil
	}
	if err := add(changed); err != nil {
		return nil, err
	}
	visitedRevisions := map[dependencyRevision]bool{}
	visitedSelectors := map[portable.ImportDependency]bool{}
	lowest := map[dependencyIdentity]int{}
	const targets = `SELECT e.envelope_hash FROM history_index_envelopes e JOIN history_archive_envelopes a USING(project_id,envelope_hash) WHERE e.project_id=? AND e.record_type=? AND e.record_id=?`
	const reverse = `SELECT d.envelope_hash FROM history_index_dependencies d JOIN history_archive_envelopes a USING(project_id,envelope_hash) WHERE d.project_id=? AND d.target_type=? AND d.target_id=?`
	for cursor := 0; cursor < len(queue); cursor++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		object, err := objects.get(ctx, queue[cursor])
		if err != nil {
			return nil, err
		}
		e := object.Envelope
		key := dependencyIdentity{e.RecordType, e.RecordID}
		revision := dependencyRevision{key, e.RecordRevision}
		if !visitedRevisions[revision] {
			visitedRevisions[revision] = true
			versions, err := readHashes(targets+` AND e.record_revision=?`, s.ProjectID, e.RecordType, e.RecordID, e.RecordRevision)
			if err != nil {
				return nil, err
			}
			if err := add(versions); err != nil {
				return nil, err
			}
			prior, seen := lowest[key]
			query := reverse + ` AND d.selection_mode='exact' AND d.target_revision=?`
			args := []any{s.ProjectID, e.RecordType, e.RecordID, e.RecordRevision}
			if !seen {
				query += ` UNION ` + reverse + ` AND d.selection_mode='any'`
				args = append(args, s.ProjectID, e.RecordType, e.RecordID)
			}
			if !seen || e.RecordRevision < prior {
				query += ` UNION ` + reverse + ` AND d.selection_mode='prior' AND d.target_revision>?`
				args = append(args, s.ProjectID, e.RecordType, e.RecordID, e.RecordRevision)
				if seen {
					query += ` AND d.target_revision<=?`
					args = append(args, prior)
				}
				lowest[key] = e.RecordRevision
			}
			owners, err := readHashes(query, args...)
			if err != nil {
				return nil, err
			}
			if err := add(owners); err != nil {
				return nil, err
			}
		}
		values, err := s.dependencySelectorsWithin(ctx, tx, []portable.EncodedEnvelope{object}, true, false)
		if err != nil {
			return nil, errReplayJournalFull
		}
		value := values[e.EnvelopeSHA256]
		if !value.Supported {
			return nil, errReplayJournalFull
		}
		for _, selector := range value.Selectors {
			if visitedSelectors[selector] {
				continue
			}
			visitedSelectors[selector] = true
			if len(visitedSelectors) > maxSemanticReplayEdges {
				return nil, errReplayJournalFull
			}
			query := targets
			args := []any{s.ProjectID, selector.RecordType, selector.RecordID}
			switch selector.Mode {
			case portable.DependencyExact:
				query += ` AND e.record_revision=?`
				args = append(args, selector.Revision)
			case portable.DependencyPrior:
				query += ` AND e.record_revision<?`
				args = append(args, selector.Revision)
			case portable.DependencyAny:
			default:
				return nil, errReplayJournalFull
			}
			targets, err := readHashes(query, args...)
			if err != nil {
				return nil, err
			}
			if err := add(targets); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(queue)
	result := make([]portable.EncodedEnvelope, 0, len(queue))
	for _, hash := range queue {
		result = append(result, objects.objects[hash])
	}
	syncwork.RecordWork(ctx, "dependency_selected_records", int64(len(result)))
	return result, nil
}
