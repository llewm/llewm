package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

var errReplayJournalFull = errors.New("complete semantic replay required")

type semanticReplayState struct {
	Version             int          `json:"version"`
	Consumer            string       `json:"consumer"`
	Schema              int64        `json:"schema"`
	Dependencies        string       `json:"dependencies"`
	Archive             archiveStamp `json:"archive"`
	CanonicalGeneration string       `json:"canonical_generation"`
	CanonicalSequence   int64        `json:"canonical_sequence"`
}

func replayJournalKey(consumer string) string { return "semantic-replay-v1:" + consumer }
func replayArchiveConsumer(consumer string) string {
	return Hash(replayJournalKey(consumer) + ":archive")
}
func replayCanonicalConsumer(consumer string) string {
	return Hash(replayJournalKey(consumer) + ":canonical")
}

// ImportArchiveJournal uses a verified current projection with a previously
// complete semantic baseline. The materializer runs outside its writer and must
// return a package-sealed complete publication snapshot. Reference import owns
// all unsupported/fork/recovery behavior. Its cursor is independent of delivery.
func (s Index) ImportArchiveJournal(ctx context.Context, a Archive, scope JournalPublicationScope, materialize func(context.Context) (JournalPublicationPlan, error)) (ImportResult, error) {
	if err := s.validateJournalScope(a, scope); err != nil {
		return ImportResult{}, err
	}
	if materialize == nil {
		return ImportResult{}, fmt.Errorf("complete semantic materializer required")
	}
	result, err := s.tryReplayJournal(ctx, a, scope)
	if !errors.Is(err, errReplayJournalFull) {
		return result, err
	}
	ctx = portable.WithEnvelopeDecodeCache(ctx)
	syncwork.RecordWork(ctx, "fallback_semantic_journal_full", 1)
	plan, err := materialize(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	if !s.validSemanticSnapshot(scope, plan) {
		return ImportResult{}, fmt.Errorf("%w: complete semantic snapshot receipt changed", ErrIndexRebuild)
	}
	result, err = s.ImportBlocks(ctx, plan.Snapshot)
	if err != nil || result.Collisions+result.DeferredRecords != 0 {
		return result, err
	}
	if err := s.seedReplayJournal(ctx, a, scope, plan); err != nil && !errors.Is(err, errReplayJournalFull) {
		return result, fmt.Errorf("%w: %v", ErrSemanticPending, err)
	}
	return result, nil
}

func (s Index) dependencyGenerationWithin(ctx context.Context, tx *sql.Tx) (string, error) {
	var token string
	err := tx.QueryRowContext(ctx, `SELECT token FROM history_index_dependency_generation WHERE project_id=?`, s.ProjectID).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (len(token) != 32 || !hex64.MatchString(token+token))) {
		return "", errReplayJournalFull
	}
	return token, err
}

func (s Index) loadReplayStateWithin(ctx context.Context, tx *sql.Tx, consumer string) (semanticReplayState, string, error) {
	var state semanticReplayState
	var body, checksum string
	err := tx.QueryRowContext(ctx, `SELECT substr(value_json,1,8193),substr(checksum,1,65) FROM history_archive_status WHERE project_id=? AND consumer=?`, s.ProjectID, Hash(replayJournalKey(consumer))).Scan(&body, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return state, "", errReplayJournalFull
	}
	if err != nil {
		return state, "", err
	}
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.DisallowUnknownFields()
	if len(body) > 8192 || Hash(body) != checksum || decoder.Decode(&state) != nil {
		return state, "", errReplayJournalFull
	}
	canonical, _ := json.Marshal(state)
	if string(canonical) != body || state.Version != 2 || state.Consumer != consumer || state.Schema <= 0 || len(state.Dependencies) != 32 || !hex64.MatchString(state.Dependencies+state.Dependencies) || !validArchiveStamp(state.Archive) || state.CanonicalGeneration == "" || len(state.CanonicalGeneration) > 256 || state.CanonicalSequence < 0 {
		return state, "", errReplayJournalFull
	}
	// Bind both independently checksummed cursors to this same baseline. A
	// valid cursor copied from a different materialization is not sufficient.
	expected, _ := json.Marshal(archiveReceipt{Project: s.ProjectID, Lineage: s.Lineage, Consumer: replayArchiveConsumer(consumer), Stamp: state.Archive})
	var archiveBody, archiveChecksum string
	err = tx.QueryRowContext(ctx, `SELECT substr(snapshot_json,1,2049),substr(checksum,1,65) FROM history_archive_cursors WHERE project_id=? AND consumer=?`, s.ProjectID, replayArchiveConsumer(consumer)).Scan(&archiveBody, &archiveChecksum)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (archiveBody != string(expected) || Hash(archiveBody) != archiveChecksum)) {
		return state, "", errReplayJournalFull
	}
	if err != nil {
		return state, "", err
	}
	var generation string
	var sequence int64
	err = tx.QueryRowContext(ctx, `SELECT substr(generation,1,257),sequence FROM history_sync_cursors WHERE project_id=? AND consumer=?`, s.ProjectID, replayCanonicalConsumer(consumer)).Scan(&generation, &sequence)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (generation != state.CanonicalGeneration || sequence != state.CanonicalSequence)) {
		return state, "", errReplayJournalFull
	}
	return state, checksum, err
}

func (s Index) rememberReplayWithin(ctx context.Context, tx *sql.Tx, a Archive, scope JournalPublicationScope, archive archiveStamp) error {
	dependencies, err := s.dependencyGenerationWithin(ctx, tx)
	if err != nil {
		return err
	}
	canonical, err := database.SyncSnapshot(ctx, tx, s.ProjectID, replayCanonicalConsumer(scope.Consumer))
	if errors.Is(err, database.ErrCanonicalTracking) {
		return errReplayJournalFull
	}
	if err != nil {
		return err
	}
	state := semanticReplayState{Version: 2, Consumer: scope.Consumer, Schema: scope.Observed.Schema, Dependencies: dependencies, Archive: archive, CanonicalGeneration: canonical.Generation, CanonicalSequence: canonical.Sequence}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	checksum := Hash(string(body))
	ok, err := a.store().rememberWithin(ctx, tx, replayArchiveConsumer(scope.Consumer), archive)
	if err != nil {
		return err
	}
	if !ok {
		return errReplayJournalFull
	}
	ok, err = database.RememberSyncCursorWithin(ctx, tx, canonical, checksum)
	if err != nil {
		return err
	}
	if !ok {
		return errReplayJournalFull
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_archive_status(project_id,consumer,value_json,checksum) VALUES(?,?,?,?) ON CONFLICT(project_id,consumer) DO UPDATE SET value_json=excluded.value_json,checksum=excluded.checksum WHERE history_archive_status.value_json IS NOT excluded.value_json OR history_archive_status.checksum IS NOT excluded.checksum`, s.ProjectID, Hash(replayJournalKey(scope.Consumer)), string(body), checksum)
	return err
}

func (s Index) seedReplayJournal(ctx context.Context, a Archive, scope JournalPublicationScope, plan JournalPublicationPlan) error {
	proof := plan.snapshot
	if proof == nil || !validObservationStamp(proof.Observed) {
		return errReplayJournalFull
	}
	return a.store().transact(ctx, func(tx *sql.Tx) error {
		current, err := s.observationStampWithin(ctx, tx, a)
		if errors.Is(err, ErrObservationTracking) || (err == nil && current != proof.Observed) {
			return errReplayJournalFull
		}
		if err != nil {
			return err
		}
		archive, err := a.store().stampWithin(ctx, tx, "ready")
		if err != nil {
			return err
		}
		if archive != proof.Archive {
			return errReplayJournalFull
		}
		objects := map[string]portable.EncodedEnvelope{}
		var size int64
		for _, block := range plan.Snapshot {
			for _, object := range block.Objects {
				hash := object.Envelope.EnvelopeSHA256
				if prior, ok := objects[hash]; ok {
					if !bytes.Equal(prior.Bytes, object.Bytes) {
						return errArchiveIntegrity
					}
					continue
				}
				objects[hash] = object
				size += int64(len(object.Bytes) + 1)
			}
		}
		if int64(len(objects)) != archive.Records || size != archive.Bytes {
			return errReplayJournalFull
		}
		all := make([]portable.EncodedEnvelope, 0, len(objects))
		for _, object := range objects {
			all = append(all, object)
		}
		local, recovered, covered, complete, err := s.completeReferenceSnapshotWithin(ctx, tx, all)
		if err != nil {
			return err
		}
		if !complete {
			return errReplayJournalFull
		}
		for _, object := range all {
			if _, supported := portable.ImportDependencies(object.Envelope); !supported {
				_, guarded := portable.ImportInvalidationGuards(object)
				_, corrected := recovered[object.Envelope.EnvelopeSHA256]
				if !guarded && !corrected {
					return errReplayJournalFull
				}
			}
		}
		for _, object := range local {
			e := object.Envelope
			if !covered[portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)] && e.RecordType != "actor_invocation" {
				return errReplayJournalFull
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_dependency_records WHERE project_id=? AND NOT EXISTS(SELECT 1 FROM history_archive_envelopes a WHERE a.project_id=history_index_dependency_records.project_id AND a.envelope_hash=history_index_dependency_records.envelope_hash)`, s.ProjectID); err != nil {
			return err
		}
		if _, err := s.dependencySelectorsWithin(ctx, tx, all, true, true); err != nil {
			return err
		}
		// Complete validation may recreate a missing disposable generation row.
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_dependency_generation(project_id,token) VALUES(?,lower(hex(randomblob(16)))) ON CONFLICT(project_id) DO UPDATE SET token=excluded.token`, s.ProjectID); err != nil {
			return err
		}
		scope.Observed = current
		return s.rememberReplayWithin(ctx, tx, a, scope, archive)
	})
}

func (s Index) tryReplayJournal(ctx context.Context, a Archive, scope JournalPublicationScope) (ImportResult, error) {
	var result ImportResult
	err := a.store().transact(ctx, func(tx *sql.Tx) error {
		current, err := s.observationStampWithin(ctx, tx, a)
		if errors.Is(err, ErrObservationTracking) || (err == nil && (!validObservationStamp(scope.Observed) || current != scope.Observed)) {
			return errReplayJournalFull
		}
		if err != nil {
			return err
		}
		state, digest, err := s.loadReplayStateWithin(ctx, tx, scope.Consumer)
		if err != nil {
			return err
		}
		dependencies, err := s.dependencyGenerationWithin(ctx, tx)
		if err != nil {
			return err
		}
		if state.Dependencies != dependencies || state.Schema != current.Schema {
			return errReplayJournalFull
		}
		_, canonical, fallback, err := database.ReadSyncChangesWithin(ctx, tx, s.ProjectID, replayCanonicalConsumer(scope.Consumer), digest, 4096)
		if err != nil {
			return err
		}
		if fallback != "" {
			return errReplayJournalFull
		}
		next, changes, fallback, err := a.store().changesWithin(ctx, tx, replayArchiveConsumer(scope.Consumer), 4096)
		if err != nil {
			return err
		}
		if fallback != "" {
			return errReplayJournalFull
		}
		objects := &replayObjectSet{archive: a, tx: tx, objects: map[string]portable.EncodedEnvelope{}}
		changed, err := s.verifyReplayCanonicalWithin(ctx, tx, canonical, objects)
		if err != nil {
			return err
		}
		added := map[string]bool{}
		for _, change := range changes {
			if change.Kind == "envelope" {
				if _, err := objects.get(ctx, change.EnvelopeHash); err != nil {
					return err
				}
				changed = append(changed, change.EnvelopeHash)
				added[change.EnvelopeHash] = true
			}
		}
		affected, err := s.replayDependencyClosureWithin(ctx, tx, objects, changed, added)
		if err != nil {
			return err
		}
		specs, err := replayImportSpecs(affected)
		if err != nil {
			return err
		}
		// Selector staging is a known mutation through compiled table guards.
		// Canonical apply must not change this cache, including via an unrelated
		// SQL trigger. Its canonical side effects are separately checked below.
		stagedDependencies, err := s.dependencyGenerationWithin(ctx, tx)
		if err != nil {
			return err
		}
		var imported int
		if len(specs) != 0 {
			now := time.Now().UTC()
			if s.Now != nil {
				now = s.Now().UTC()
			}
			plan, receipt, err := portable.ImportMemoryRecordsTx(ctx, tx, s.ProjectID, specs, now, portable.RecoveryComparisonGuarded)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrSemanticPending, err)
			}
			if !plan.ApplyAllowed {
				return errReplayJournalFull
			}
			if receipt != nil {
				imported = receipt.AppendedRecords
			}
		}
		_, afterChanges, fallback, err := database.ReadSyncChangesWithin(ctx, tx, s.ProjectID, replayCanonicalConsumer(scope.Consumer), digest, 4096)
		if err != nil {
			return err
		}
		if fallback != "" {
			return errReplayJournalFull
		}
		if _, err := s.verifyReplayCanonicalWithin(ctx, tx, afterChanges, objects); err != nil {
			return err
		}
		afterDependencies, err := s.dependencyGenerationWithin(ctx, tx)
		if err != nil {
			return err
		}
		afterProjection, err := s.observationStampWithin(ctx, tx, a)
		if errors.Is(err, ErrObservationTracking) || (err == nil && (afterProjection != current || afterDependencies != stagedDependencies)) {
			return errReplayJournalFull
		}
		if err != nil {
			return err
		}
		if err := s.rememberReplayWithin(ctx, tx, a, scope, next); err != nil {
			return err
		}
		result.ImportedRecords = imported
		return nil
	})
	if err == nil {
		syncwork.RecordWork(ctx, "semantic_journal_delta_cycles", 1)
	} else {
		result = ImportResult{}
	}
	return result, err
}

func (s Index) verifyReplayCanonicalWithin(ctx context.Context, tx *sql.Tx, changes []database.SyncChange, objects *replayObjectSet) ([]string, error) {
	current, qualified, err := portable.CanonicalJournalEnvelopesTx(ctx, tx, s.ProjectID, changes, portable.RecoveryComparisonGuarded)
	if err != nil {
		return nil, err
	}
	if !qualified {
		return nil, errReplayJournalFull
	}
	hashes := make([]string, 0, len(current))
	for _, object := range current {
		hash := object.Envelope.EnvelopeSHA256
		archived, err := objects.get(ctx, hash)
		if errors.Is(err, sql.ErrNoRows) {
			// A concurrent canonical append may not have been captured yet.
			return nil, errReplayJournalFull
		}
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(archived.Bytes, object.Bytes) {
			return nil, errArchiveIntegrity
		}
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

func replayImportSpecs(objects []portable.EncodedEnvelope) ([]portable.EnvelopeSpec, error) {
	seen := map[string]bool{}
	var specs []portable.EnvelopeSpec
	for _, object := range objects {
		e := object.Envelope
		key := portable.RecordKey(e.RecordType, e.RecordID, e.RecordRevision)
		if seen[key] {
			// Fork recording and schema-equivalent competing observations remain
			// the reference importer's responsibility.
			return nil, errReplayJournalFull
		}
		seen[key] = true
		dependencies, supported := portable.ImportDependencies(e)
		if !supported || e.RecordType == "work_narrative_supersession" || e.RecordType == "fork_resolution" || strings.HasSuffix(e.RecordType, "_fork_resolution") || (e.RecordType == "decision" && len(dependencies) != 0) {
			return nil, errReplayJournalFull
		}
		spec, err := portable.DecodeMemoryEnvelope(object.Bytes)
		if err != nil {
			return nil, errReplayJournalFull
		}
		specs = append(specs, spec)
	}
	return specs, nil
}
