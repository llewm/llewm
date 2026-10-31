package portable

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// A selection applies only to explicitly marked primary export reads. Exact
// child/dependency reads keep their ordinary semantics. Unknown journal tables,
// updates and deletions require the complete exporter, not a guessed closure.
type deltaSource struct {
	queryer
	keys map[string][][]any
}

// primaryExportSource opts a package-owned single-table export reader into the
// same selection as the portable-owned readers. Only declared root queries are
// accepted: changes to a reader's query shape fail closed, rather than silently
// filtering dependency reads or replaying every historical row.
type primaryExportSource struct {
	queryer
	tables []string
}

func selectedExportSource(source queryer, tables ...string) queryer {
	if _, selected := source.(*deltaSource); !selected {
		return source
	}
	return &primaryExportSource{queryer: source, tables: tables}
}

func (s *primaryExportSource) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if len(args) == 1 {
		if project, ok := args[0].(string); ok {
			for _, table := range s.tables {
				if strings.HasPrefix(query, "SELECT ") && strings.Contains(query, " FROM "+table+" WHERE project_id=? ORDER BY ") {
					return exportRows(ctx, s.queryer, table, query, project)
				}
			}
		}
	}
	return nil, fmt.Errorf("unqualified primary export query")
}

var deltaColumns = map[string][]string{
	"checkpoints": {"id"}, "checkpoint_actions": {"id"},
	"checkpoint_action_transitions": {"t.id"},
	"decision_records":              {"id"}, "topic_revisions": {"id", "revision"},
	"topic_aliases": {"alias"}, "topic_links": {"id"},
	"track_plan_revisions": {"id", "revision"},
	"reviewed_procedures":  {"procedure_id", "revision"},
	"work_items":           {"id"}, "work_item_dispositions": {"id"}, "work_item_links": {"id"},
	"work_narratives": {"id"},
	"roadmap_nodes":   {"node_id", "revision"}, "roadmap_file_bindings": {"id"},
	"roadmap_node_links": {"id"}, "roadmap_review_routines": {"routine_id", "revision"},
	"roadmap_sync_runs": {"id"}, "roadmap_releases": {"release_id", "revision"},
	"lease_stamps": {"record_kind", "record_id"}, "stale_write_resolutions": {"id"},
	"checkpoint_contexts": {"checkpoint_id"}, "checkpoint_branches": {"checkpoint_id"},
	"resources": {"id", "revision"}, "resource_notes": {"id"},
	"pins": {"id"}, "coverage_cells": {"id"}, "fork_resolutions": {"id"},
}

func exportRows(ctx context.Context, source queryer, table, query, project string) (*sql.Rows, error) {
	selection, ok := source.(*deltaSource)
	if !ok {
		return source.QueryContext(ctx, query, project)
	}
	columns, ok := deltaColumns[table]
	if !ok {
		return nil, fmt.Errorf("unqualified delta export table %s", table)
	}
	// All marked queries have a project predicate and a final ORDER BY. Keep
	// placeholders bound and use only the static column names above.
	prefix, order, ok := strings.Cut(query, " ORDER BY ")
	if !ok {
		return nil, fmt.Errorf("delta export query lacks explicit ordering")
	}
	args := []any{project}
	var predicates []string
	for _, key := range selection.keys[table] {
		if len(key) != len(columns) {
			return nil, fmt.Errorf("delta export key arity mismatch")
		}
		var terms []string
		for i, column := range columns {
			terms = append(terms, column+"=?")
			args = append(args, key[i])
		}
		predicates = append(predicates, "("+strings.Join(terms, " AND ")+")")
	}
	predicate := "0"
	if len(predicates) != 0 {
		predicate = strings.Join(predicates, " OR ")
	}
	return source.QueryContext(ctx, prefix+" AND ("+predicate+") ORDER BY "+order, args...)
}

func exportRetainedRows(ctx context.Context, source queryer, query, project string) (*sql.Rows, error) {
	if _, selected := source.(*deltaSource); selected {
		// Delta loaders reconstruct only selected operational rows. Overlay
		// retained bytes by exact identity afterwards, without scanning history.
		query += " AND 0"
	}
	return source.QueryContext(ctx, query, project)
}

func selectExportChanges(project string, changes []database.SyncChange) (map[string][][]any, bool) {
	keys := map[string][][]any{}
	seen := map[string]bool{}
	for _, change := range changes {
		switch change.Table {
		case "history_index_blocks", "history_index_publications", "history_index_envelopes", "history_index_block_members", "history_index_refs":
			continue
		}
		if change.Operation != "insert" {
			return nil, false
		}
		var key []any
		decoder := json.NewDecoder(strings.NewReader(change.Key))
		decoder.UseNumber()
		if decoder.Decode(&key) != nil || len(key) == 0 {
			return nil, false
		}
		table := change.Table
		// Project-owned composite keys start with the project ID. Globally
		// unique IDs do not; their journal row already established ownership.
		switch table {
		case "decision_records", "decision_links", "topic_revisions", "topic_aliases", "topic_links", "track_plan_revisions", "track_plan_items", "track_plan_item_dependencies", "track_plan_work_items", "reviewed_procedures", "work_items", "work_item_dispositions", "work_item_links", "work_item_tags", "work_narrative_claims", "work_narrative_anchors", "work_narrative_citations", "roadmap_nodes", "roadmap_review_routines", "roadmap_releases", "roadmap_release_entries", "lease_stamps", "stale_write_resolutions", "checkpoint_contexts", "checkpoint_branches", "resources", "coverage_cells", "fork_resolutions":
			if key[0] != project {
				return nil, false
			}
			key = key[1:]
		}
		switch table {
		case "decision_links":
			table = "decision_records"
		case "track_plan_items", "track_plan_item_dependencies", "track_plan_work_items":
			table = "track_plan_revisions"
		case "work_item_tags":
			table = "work_items"
		case "work_narrative_claims", "work_narrative_anchors", "work_narrative_citations":
			table = "work_narratives"
		case "roadmap_release_entries":
			table = "roadmap_releases"
		}
		columns, ok := deltaColumns[table]
		if !ok || len(key) < len(columns) {
			return nil, false
		}
		key = key[:len(columns)]
		for i, value := range key {
			switch v := value.(type) {
			case string:
				if v == "" {
					return nil, false
				}
			case json.Number:
				n, err := v.Int64()
				if err != nil || n < 1 {
					return nil, false
				}
				key[i] = n
			default:
				return nil, false
			}
		}
		encoded, _ := json.Marshal(key)
		identity := table + "\x00" + string(encoded)
		if !seen[identity] {
			// Bound SQL expression depth and writer time. Large batches use
			// the complete exporter rather than one unbounded OR expression.
			if len(keys[table]) >= 128 {
				return nil, false
			}
			keys[table] = append(keys[table], key)
			seen[identity] = true
		}
	}
	return keys, true
}

// EncodeMemoryDelta consumes a previously validated journal batch. The returned
// cursor includes this export's own retention writes, from the same transaction.
// It may be acknowledged only after durable archive publication. A false result
// requires complete export; it never advances the caller's cursor.
func EncodeMemoryDelta(ctx context.Context, db *sql.DB, expected database.SyncCursor, changes []database.SyncChange) (MemoryExport, database.SyncCursor, bool, error) {
	keys, qualified := selectExportChanges(expected.Project, changes)
	if !qualified {
		syncwork.RecordWork(ctx, "fallback_delta_unqualified_changes", 1)
		return MemoryExport{}, expected, false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	defer tx.Rollback()
	// Reserve the writer even with a connection using deferred transactions.
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET name=name WHERE id=? AND 0`, expected.Project); err != nil {
		return MemoryExport{}, expected, false, err
	}
	current, err := database.SyncSnapshot(ctx, tx, expected.Project, expected.Consumer)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	if current.Generation != expected.Generation || current.Sequence != expected.Sequence {
		syncwork.RecordWork(ctx, "fallback_delta_snapshot_changed", 1)
		return MemoryExport{}, expected, false, nil
	}
	// Recovery can substitute identities and bytes beyond the changed row.
	// Keep its complete graph validation until that closure is qualified.
	var recovery bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM redaction_recoveries WHERE project_id=?)`, expected.Project).Scan(&recovery); err != nil {
		return MemoryExport{}, expected, false, err
	}
	if recovery {
		syncwork.RecordWork(ctx, "fallback_delta_recovery", 1)
		return MemoryExport{}, expected, false, nil
	}
	identity, err := identityFrom(ctx, tx, expected.Project)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	root, err := projectRootFrom(ctx, tx, expected.Project)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	result := MemoryExport{Identity: identity}
	if err = tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&result.DatabaseSchema); err != nil {
		return MemoryExport{}, expected, false, err
	}
	source := &deltaSource{queryer: tx, keys: keys}
	var specs []EnvelopeSpec
	for _, load := range []func(context.Context, queryer, string, string, string) ([]EnvelopeSpec, error){loadCheckpointSpecs, loadLedgerSpecs, loadTrackPlanSpecs, loadProcedureSpecs, loadReviewSpecs, loadReleaseSpecs, loadLeaseSpecs, loadResourceSpecs, loadPinSpecs, loadCoverageCellSpecs, loadForkResolutionSpecs} {
		values, e := load(ctx, source, expected.Project, root, identity.LineageID)
		if e != nil {
			return MemoryExport{}, expected, false, e
		}
		specs = append(specs, values...)
	}
	for _, load := range []func(context.Context, queryer, string, string) ([]EnvelopeSpec, error){loadCheckpointContextSpecs, loadCheckpointBranchesSpecs} {
		values, e := load(ctx, source, expected.Project, identity.LineageID)
		if e != nil {
			return MemoryExport{}, expected, false, e
		}
		specs = append(specs, values...)
	}
	roadmap, invocations, err := loadRoadmapSpecs(ctx, source, expected.Project, root, identity.LineageID)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	specs = append(specs, roadmap...)
	narratives, missing, err := loadNarrativeAndInvocationSpecs(ctx, source, expected.Project, root, identity.LineageID, invocations, false)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	if len(missing) != 0 {
		syncwork.RecordWork(ctx, "fallback_delta_missing_actors", 1)
		return MemoryExport{}, expected, false, nil
	}
	specs = append(specs, narratives...)
	syncwork.RecordWork(ctx, "operational_delta_loads", 1)
	syncwork.RecordWork(ctx, "operational_specs_loaded", int64(len(specs)))
	for _, spec := range specs {
		var body string
		err = tx.QueryRowContext(ctx, `SELECT envelope_json FROM portable_envelopes WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=?`, expected.Project, spec.RecordType, spec.RecordID, spec.RecordRevision).Scan(&body)
		var encoded EncodedEnvelope
		if err == nil {
			var retained EnvelopeSpec
			syncwork.RecordWork(ctx, "retained_envelopes_decoded", 1)
			syncwork.RecordWork(ctx, "retained_envelope_bytes_decoded", int64(len(body)))
			retained, encoded, err = decodeMemoryEnvelope([]byte(body))
			if err == nil && (retained.LineageID != identity.LineageID || retained.RecordType != spec.RecordType || retained.RecordID != spec.RecordID || retained.RecordRevision != spec.RecordRevision) {
				err = fmt.Errorf("retained portable envelope identity mismatch")
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			syncwork.RecordWork(ctx, "snapshot_envelope_encodes", 1)
			encoded, err = EncodeEnvelope(spec)
			if err == nil {
				err = retainEnvelope(ctx, tx, expected.Project, "export", encoded)
			}
		}
		if err != nil {
			return MemoryExport{}, expected, false, err
		}
		result.Envelopes = append(result.Envelopes, encoded)
	}
	current, err = database.SyncSnapshot(ctx, tx, expected.Project, expected.Consumer)
	if err != nil {
		return MemoryExport{}, expected, false, err
	}
	return result, current, true, tx.Commit()
}
