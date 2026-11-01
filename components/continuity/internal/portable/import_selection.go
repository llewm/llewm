package portable

import (
	"context"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// loadImportSelection reconstructs only local identities compared by this
// bundle. It does not certify any omitted canonical state. The ordinary apply
// dependency validator still checks every append; callers needing a complete
// local-state proof must use loadOperationalSpecs instead.
func loadImportSelection(ctx context.Context, source queryer, project, root, lineage string, objects []EncodedEnvelope, validated snapshotEncodings) ([]EnvelopeSpec, bool, error) {
	if len(objects) > 128 {
		return nil, false, nil
	}
	selection := &recordSelectionSource{queryer: source, keys: map[string][][]any{}, retainedKeys: []Envelope{}}
	actors := map[string]struct{}{}
	wanted := map[string]bool{}
	seenRoots := map[string]bool{}
	for _, object := range objects {
		e := object.Envelope
		identity := recordKey(e)
		if wanted[identity] {
			continue
		}
		wanted[identity] = true
		selection.retainedKeys = append(selection.retainedKeys, e)
		var table string
		key := []any{e.RecordID}
		switch e.RecordType {
		case "decision":
			table = "decision_records"
		case "topic_revision":
			table, key = "topic_revisions", []any{e.RecordID, e.RecordRevision}
		case "topic_alias":
			table = "topic_aliases"
		case "topic_link":
			table = "topic_links"
		case "checkpoint":
			table = "checkpoints"
		case "checkpoint_action":
			table = "checkpoint_actions"
		case "checkpoint_action_transition":
			table = "checkpoint_action_transitions"
		case "checkpoint_context":
			table = "checkpoint_contexts"
		case "checkpoint_branches":
			table = "checkpoint_branches"
		case "track_plan":
			table, key = "track_plan_revisions", []any{e.RecordID, e.RecordRevision}
		case "procedure":
			table, key = "reviewed_procedures", []any{e.RecordID, e.RecordRevision}
		case "resource":
			table, key = "resources", []any{e.RecordID, e.RecordRevision}
		case "resource_note":
			table = "resource_notes"
		case "work_item":
			table = "work_items"
		case "work_item_disposition":
			table = "work_item_dispositions"
		case "work_item_link":
			table = "work_item_links"
		case "work_narrative", "work_narrative_supersession":
			table = "work_narratives"
		case "actor_invocation":
			actors[e.RecordID] = struct{}{}
			continue
		case "roadmap_node":
			table, key = "roadmap_nodes", []any{e.RecordID, e.RecordRevision}
		case "roadmap_binding":
			table = "roadmap_file_bindings"
		case "roadmap_link":
			table = "roadmap_node_links"
		case "roadmap_review_routine":
			table, key = "roadmap_review_routines", []any{e.RecordID, e.RecordRevision}
		case "roadmap_sync":
			table = "roadmap_sync_runs"
		case "roadmap_release":
			table, key = "roadmap_releases", []any{e.RecordID, e.RecordRevision}
		case "pin":
			table = "pins"
		case "coverage_cell":
			table = "coverage_cells"
		case "lease_stamp":
			kind, id, ok := strings.Cut(e.RecordID, ":")
			if !ok || kind == "" || id == "" {
				return nil, false, nil
			}
			table, key = "lease_stamps", []any{kind, id}
		case "stale_write_resolution":
			table = "stale_write_resolutions"
		case "fork_resolution", "revision_fork_resolution", "supersession_fork_resolution":
			table = "fork_resolutions"
		default:
			return nil, false, nil
		}
		// Families sharing one operational row are loaded once, then filtered
		// back to the exact requested portable identities below.
		rootKey := table + "\x00" + e.RecordID
		if len(key) == 2 {
			rootKey = table + "\x00" + identity
		}
		if !seenRoots[rootKey] {
			selection.keys[table] = append(selection.keys[table], key)
			seenRoots[rootKey] = true
		}
	}
	var recovery bool
	if err := source.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM redaction_recoveries WHERE project_id=?)`, project).Scan(&recovery); err != nil {
		return nil, true, err
	}
	if recovery {
		return nil, false, nil
	}
	specs, _, err := loadSelectedOperationalSpecs(ctx, selection, project, root, lineage, actors)
	if err != nil {
		return nil, true, err
	}
	syncwork.RecordWork(ctx, "operational_selected_loads", 1)
	syncwork.RecordWork(ctx, "operational_specs_loaded", int64(len(specs)))
	selected := make([]EnvelopeSpec, 0, len(specs))
	for _, spec := range specs {
		if wanted[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] {
			selected = append(selected, spec)
		}
	}
	selected, err = retainedSpecs(ctx, selection, project, lineage, selected, validated)
	return selected, true, err
}
