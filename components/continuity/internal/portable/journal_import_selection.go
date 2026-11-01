package portable

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// CanonicalJournalEnvelopesTx reconstructs the curated identities affected by a
// qualified canonical journal batch in the caller's snapshot. Retention and
// invocation inserts are checked through the same operational readers. This
// performs no writes or acknowledgements, and does not certify omitted state.
// The caller must hold the writer from reading its batch through its commit.
func CanonicalJournalEnvelopesTx(ctx context.Context, tx *sql.Tx, project string, changes []database.SyncChange) ([]EncodedEnvelope, bool, error) {
	if len(changes) > 4096 {
		return nil, false, nil
	}
	var roots []database.SyncChange
	var extra []EncodedEnvelope
	for _, change := range changes {
		if change.Table != "portable_envelopes" && change.Table != "actor_invocations" {
			roots = append(roots, change)
			continue
		}
		if change.Operation != "insert" {
			return nil, false, nil
		}
		decoder := json.NewDecoder(strings.NewReader(change.Key))
		decoder.UseNumber()
		var key []any
		if decoder.Decode(&key) != nil {
			return nil, false, nil
		}
		var identity Envelope
		if change.Table == "actor_invocations" {
			if len(key) != 1 {
				return nil, false, nil
			}
			id, ok := key[0].(string)
			if !ok || id == "" {
				return nil, false, nil
			}
			identity = Envelope{RecordType: "actor_invocation", RecordID: id}
		} else {
			if len(key) != 4 || key[0] != project {
				return nil, false, nil
			}
			kind, kindOK := key[1].(string)
			id, idOK := key[2].(string)
			number, numberOK := key[3].(json.Number)
			revision, err := strconv.Atoi(string(number))
			if !kindOK || !idOK || !numberOK || kind == "" || id == "" || err != nil || revision < 0 {
				return nil, false, nil
			}
			identity = Envelope{RecordType: kind, RecordID: id, RecordRevision: revision}
			var retained bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM portable_envelopes WHERE project_id=? AND record_type=? AND record_id=? AND record_revision=?)`, project, kind, id, revision).Scan(&retained); err != nil {
				return nil, false, err
			}
			if !retained {
				return nil, false, nil
			}
		}
		extra = append(extra, EncodedEnvelope{Envelope: identity})
		if len(extra) > 128 {
			return nil, false, nil
		}
	}
	keys, qualified := selectExportChanges(project, roots)
	if !qualified {
		return nil, false, nil
	}
	if len(keys) == 0 && len(extra) == 0 {
		return nil, true, nil
	}
	var recovery bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM redaction_recoveries WHERE project_id=?)`, project).Scan(&recovery); err != nil {
		return nil, false, err
	}
	if recovery {
		return nil, false, nil
	}
	identity, err := identityFrom(ctx, tx, project)
	if err != nil {
		return nil, false, err
	}
	root, err := projectRootFrom(ctx, tx, project)
	if err != nil {
		return nil, false, err
	}
	validated := make(snapshotEncodings)
	var specs []EnvelopeSpec
	if len(keys) != 0 {
		selection := &recordSelectionSource{queryer: tx, keys: keys}
		var missing []Reference
		specs, missing, err = loadSelectedOperationalSpecs(ctx, selection, project, root, identity.LineageID, nil)
		if err != nil || len(missing) != 0 {
			return nil, false, err
		}
		syncwork.RecordWork(ctx, "operational_journal_loads", 1)
		syncwork.RecordWork(ctx, "operational_specs_loaded", int64(len(specs)))
		selection.retainedKeys = []Envelope{}
		for _, spec := range specs {
			selection.retainedKeys = append(selection.retainedKeys, Envelope{RecordType: spec.RecordType, RecordID: spec.RecordID, RecordRevision: spec.RecordRevision})
		}
		if len(specs) > 128 {
			return nil, false, nil
		}
		specs, err = retainedSpecs(ctx, selection, project, identity.LineageID, specs, validated)
		if err != nil {
			return nil, false, err
		}
	}
	if len(extra) != 0 {
		selected, used, err := loadImportSelection(ctx, tx, project, root, identity.LineageID, extra, validated)
		if err != nil || !used {
			return nil, false, err
		}
		present := map[string]bool{}
		for _, spec := range selected {
			present[RecordKey(spec.RecordType, spec.RecordID, spec.RecordRevision)] = true
		}
		for _, object := range extra {
			if !present[recordKey(object.Envelope)] {
				return nil, false, nil
			}
		}
		specs = append(specs, selected...)
	}
	unique := map[string]EncodedEnvelope{}
	var total int
	for _, spec := range specs {
		object, err := validated.encode(ctx, spec)
		if err != nil {
			return nil, false, err
		}
		key := recordKey(object.Envelope)
		if prior, exists := unique[key]; exists {
			if !bytes.Equal(prior.Bytes, object.Bytes) {
				return nil, false, nil
			}
			continue
		}
		unique[key] = object
		total += len(object.Bytes)
		if len(unique) > 128 || total > MaxRecordsBytes {
			return nil, false, nil
		}
	}
	result := make([]EncodedEnvelope, 0, len(unique))
	for _, object := range unique {
		result = append(result, object)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Envelope.EnvelopeSHA256 < result[j].Envelope.EnvelopeSHA256 })
	syncwork.RecordWork(ctx, "canonical_journal_selected_records", int64(len(result)))
	return result, true, nil
}
