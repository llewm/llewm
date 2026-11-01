package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

const maxPublicationPending = 512

var errPublicationJournalFull = errors.New("complete publication materialization required")

// JournalPublicationScope is supplied only by a verified transport holding its
// clone lock. Callbacks witness immutable local Git trees, not remote delivery.
// Consumer binds build, clone, lineage, endpoint, policy and interpretation.
type JournalPublicationScope struct {
	Endpoint, Consumer, Tree string
	Relay                    bool
	Observed                 ObservationStamp
	HasBlock                 func(context.Context, string) (bool, error)
	AppendOnly               func(context.Context, string) (bool, error)
}

type JournalPublicationPlan struct {
	Blocks, Snapshot []Block
	Observed         ObservationStamp
	Delta            bool
	snapshot         *semanticSnapshotReceipt
}

type publicationJournal struct {
	Version  int              `json:"version"`
	Consumer string           `json:"consumer"`
	Endpoint string           `json:"endpoint"`
	Relay    bool             `json:"relay"`
	Observed ObservationStamp `json:"observed"`
	Tree     string           `json:"tree"`
	Pending  []string         `json:"pending"`
}

func publicationJournalKey(consumer string) string { return "publication-journal-v1:" + consumer }
func publicationJournalCursor(consumer string) string {
	return Hash(publicationJournalKey(consumer) + ":changes")
}
func validPublicationTree(tree string) bool {
	if tree == "" {
		return true
	}
	if len(tree) != 40 && len(tree) != 64 {
		return false
	}
	_, err := hex.DecodeString(tree)
	return err == nil && tree == string(bytes.ToLower([]byte(tree)))
}
func validObservationStamp(stamp ObservationStamp) bool {
	return stamp.Schema > 0 && len(stamp.Inventory) == 32 && hex64.MatchString(stamp.Inventory+stamp.Inventory) && hex64.MatchString(stamp.ArchiveEpoch)
}
func (s Index) validateJournalScope(a Archive, scope JournalPublicationScope) error {
	if err := s.observationBinding(a); err != nil {
		return err
	}
	if !hex64.MatchString(scope.Consumer) || scope.Endpoint == "" || !validPublicationTree(scope.Tree) || scope.HasBlock == nil || scope.AppendOnly == nil {
		return fmt.Errorf("bound publication transport witness required")
	}
	return nil
}
func (s Index) loadPublicationJournalWithin(ctx context.Context, tx *sql.Tx, scope JournalPublicationScope) (publicationJournal, bool, error) {
	var state publicationJournal
	var body, checksum string
	err := tx.QueryRowContext(ctx, `SELECT substr(value_json,1,65537),substr(checksum,1,65) FROM history_archive_status WHERE project_id=? AND consumer=?`, s.ProjectID, Hash(publicationJournalKey(scope.Consumer))).Scan(&body, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if len(body) > 64<<10 || Hash(body) != checksum {
		return state, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil {
		return state, false, nil
	}
	canonical, _ := json.Marshal(state)
	if !bytes.Equal(canonical, []byte(body)) || state.Version != 1 || state.Consumer != scope.Consumer || state.Endpoint != Hash(scope.Endpoint) || state.Relay != scope.Relay || !validPublicationTree(state.Tree) || !validObservationStamp(state.Observed) || len(state.Pending) > maxPublicationPending || !sort.StringsAreSorted(state.Pending) {
		return state, false, nil
	}
	for i, hash := range state.Pending {
		if !hex64.MatchString(hash) || (i > 0 && hash == state.Pending[i-1]) {
			return state, false, nil
		}
	}
	return state, true, nil
}
func (s Index) savePublicationJournalWithin(ctx context.Context, tx *sql.Tx, state publicationJournal) error {
	body, err := json.Marshal(state)
	if err != nil || len(body) > 64<<10 {
		return fmt.Errorf("publication journal exceeds bounds")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_archive_status(project_id,consumer,value_json,checksum) VALUES(?,?,?,?) ON CONFLICT(project_id,consumer) DO UPDATE SET value_json=excluded.value_json,checksum=excluded.checksum WHERE history_archive_status.value_json IS NOT excluded.value_json OR history_archive_status.checksum IS NOT excluded.checksum`, s.ProjectID, Hash(publicationJournalKey(state.Consumer)), string(body), Hash(string(body)))
	return err
}

// PlanJournalPublication consumes only changed archive observations when the
// complete prior materialization is still proved. Pending blocks remain in its
// receipt until a later verified local Git tree contains them. Full fallback
// uses the reference planner and binds its exact archive snapshot before ack.
func (s Index) PlanJournalPublication(ctx context.Context, a Archive, scope JournalPublicationScope, wantSnapshot bool) (JournalPublicationPlan, error) {
	if err := s.validateJournalScope(a, scope); err != nil {
		return JournalPublicationPlan{}, err
	}
	result, err := s.tryPublicationJournal(ctx, a, scope, wantSnapshot)
	if !errors.Is(err, errPublicationJournalFull) {
		return result, err
	}
	syncwork.RecordWork(ctx, "fallback_publication_journal_full", 1)
	return s.fullPublicationJournal(ctx, a, scope, wantSnapshot)
}

func (s Index) tryPublicationJournal(ctx context.Context, a Archive, scope JournalPublicationScope, wantSnapshot bool) (JournalPublicationPlan, error) {
	var result JournalPublicationPlan
	err := a.store().transact(ctx, func(tx *sql.Tx) error {
		current, err := s.observationStampWithin(ctx, tx, a)
		if err != nil {
			if errors.Is(err, ErrObservationTracking) {
				return errPublicationJournalFull
			}
			return err
		}
		if !validObservationStamp(scope.Observed) || current != scope.Observed {
			return errPublicationJournalFull
		}
		state, known, err := s.loadPublicationJournalWithin(ctx, tx, scope)
		if err != nil {
			return err
		}
		if !known || state.Observed != current {
			return errPublicationJournalFull
		}
		if state.Tree != scope.Tree {
			ok, err := scope.AppendOnly(ctx, state.Tree)
			if err != nil || !ok {
				return errPublicationJournalFull
			}
		}
		next, changes, fallback, err := a.store().changesWithin(ctx, tx, publicationJournalCursor(scope.Consumer), 4096)
		if err != nil {
			return err
		}
		if fallback != "" {
			return errPublicationJournalFull
		}
		pending := map[string]Block{}
		covered := map[string]bool{}
		for _, hash := range state.Pending {
			present, err := scope.HasBlock(ctx, hash)
			if err != nil {
				return err
			}
			if present {
				continue
			}
			block, err := s.catalogBlockWithin(ctx, tx, hash)
			if err != nil {
				return err
			}
			for _, object := range block.Objects {
				held, err := a.store().readObjectWithin(ctx, tx, object.Envelope.EnvelopeSHA256)
				if err != nil || !bytes.Equal(held.Bytes, object.Bytes) {
					return errArchiveIntegrity
				}
				allowed, err := a.store().authorizedWithin(ctx, tx, object.Envelope.EnvelopeSHA256, Hash(scope.Endpoint), scope.Relay)
				if err != nil {
					return err
				}
				if !allowed {
					return errPublicationJournalFull
				}
				covered[object.Envelope.EnvelopeSHA256] = true
			}
			pending[hash] = block
		}
		seen := map[string]bool{}
		var semantic, transport []portable.EncodedEnvelope
		for _, change := range changes {
			hash := change.EnvelopeHash
			if seen[hash] {
				continue
			}
			seen[hash] = true
			object, err := a.store().readObjectWithin(ctx, tx, hash)
			if err != nil {
				return err
			}
			semanticExists, candidates, err := s.publicationMembershipWithin(ctx, tx, hash, scope.Endpoint)
			if err != nil {
				return err
			}
			if !semanticExists {
				semantic = append(semantic, object)
			}
			allowed, err := a.store().authorizedWithin(ctx, tx, hash, Hash(scope.Endpoint), scope.Relay)
			if err != nil {
				return err
			}
			if !allowed || covered[hash] {
				continue
			}
			for _, candidate := range candidates {
				present, err := scope.HasBlock(ctx, candidate)
				if err != nil {
					return err
				}
				if present {
					covered[hash] = true
					break
				}
			}
			if !covered[hash] {
				transport = append(transport, object)
			}
		}
		semanticBlocks, err := packValidatedBlocks(s.Lineage, semantic)
		if err != nil {
			return err
		}
		transportBlocks, err := packValidatedBlocks(s.Lineage, transport)
		if err != nil {
			return err
		}
		for _, block := range transportBlocks {
			pending[block.Hash] = block
		}
		if len(pending) > maxPublicationPending {
			return errPublicationJournalFull
		}
		for _, block := range semanticBlocks {
			if err := s.stageJournalBlockWithin(ctx, tx, Hash("local-snapshot:"+s.Lineage), block); err != nil {
				return err
			}
		}
		for _, block := range transportBlocks {
			if err := s.stageJournalBlockWithin(ctx, tx, Hash(scope.Endpoint), block); err != nil {
				return err
			}
		}
		after, err := s.observationStampWithin(ctx, tx, a)
		if err != nil {
			return err
		}
		if after.ArchiveEpoch != current.ArchiveEpoch || after.Schema != current.Schema {
			return errPublicationJournalFull
		}
		state.Observed, state.Tree = after, scope.Tree
		state.Pending = nil
		for hash := range pending {
			state.Pending = append(state.Pending, hash)
		}
		sort.Strings(state.Pending)
		for _, hash := range state.Pending {
			result.Blocks = append(result.Blocks, pending[hash])
		}
		if wantSnapshot {
			result.Snapshot, err = s.semanticSnapshotWithin(ctx, tx)
			if err != nil {
				return err
			}
			result.snapshot = s.sealSemanticSnapshot(scope.Consumer, next, after, result.Snapshot)
		}
		remembered, err := a.store().rememberWithin(ctx, tx, publicationJournalCursor(scope.Consumer), next)
		if err != nil {
			return err
		}
		if !remembered {
			return errPublicationJournalFull
		}
		if err := s.savePublicationJournalWithin(ctx, tx, state); err != nil {
			return err
		}
		result.Observed, result.Delta = after, true
		return nil
	})
	if err != nil {
		return JournalPublicationPlan{}, err
	}
	return result, nil
}

func (s Index) stageJournalBlockWithin(ctx context.Context, tx *sql.Tx, endpointHash string, block Block) error {
	if err := s.materializeBlockWithin(ctx, tx, block); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, endpointHash, block.Hash)
	return err
}

func (s Index) fullPublicationJournal(ctx context.Context, a Archive, scope JournalPublicationScope, wantSnapshot bool) (JournalPublicationPlan, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var snapshot archiveStamp
		if err := a.store().view(ctx, func(tx *sql.Tx) error {
			var err error
			snapshot, err = a.store().stampWithin(ctx, tx, "ready")
			return err
		}); err != nil {
			return JournalPublicationPlan{}, err
		}
		blocks, inventory, planned, err := s.PlanObservedPublicationWithInventory(ctx, a, scope.Endpoint, scope.Relay, scope.Observed)
		if err != nil {
			return JournalPublicationPlan{}, err
		}
		unique := map[string]portable.EncodedEnvelope{}
		var materializedBytes int64
		for _, block := range inventory {
			for _, object := range block.Objects {
				hash := object.Envelope.EnvelopeSHA256
				if previous, exists := unique[hash]; exists {
					if !bytes.Equal(previous.Bytes, object.Bytes) {
						return JournalPublicationPlan{}, errArchiveIntegrity
					}
					continue
				}
				unique[hash] = object
				materializedBytes += int64(len(object.Bytes) + 1)
			}
		}
		if int64(len(unique)) != snapshot.Records || materializedBytes != snapshot.Bytes {
			// A concurrent archive arrival may have changed the reference scan.
			// No cursor is acknowledged until its exact complete snapshot matches.
			continue
		}
		all := make([]portable.EncodedEnvelope, 0, len(unique))
		for _, object := range unique {
			all = append(all, object)
		}
		inventory, err = packValidatedBlocks(s.Lineage, all)
		if err != nil {
			return JournalPublicationPlan{}, err
		}
		result := JournalPublicationPlan{Snapshot: inventory}
		if wantSnapshot {
			result.snapshot = s.sealSemanticSnapshot(scope.Consumer, snapshot, ObservationStamp{}, inventory)
		}
		err = a.store().transact(ctx, func(tx *sql.Tx) error {
			current, err := a.store().stampWithin(ctx, tx, "ready")
			if err != nil {
				return err
			}
			if current != snapshot {
				return errPublicationChanged
			}
			before, trackingErr := s.observationStampWithin(ctx, tx, a)
			if trackingErr != nil && !errors.Is(trackingErr, ErrObservationTracking) {
				return trackingErr
			}
			advance := trackingErr == nil && validObservationStamp(planned) && before == planned
			// This local-only complete projection replaces any partial old snapshot
			// catalog. No wire blocks, archive bodies or provenance are removed.
			if _, err := tx.ExecContext(ctx, `DELETE FROM history_index_publications WHERE project_id=? AND endpoint_hash=?`, s.ProjectID, Hash("local-snapshot:"+s.Lineage)); err != nil {
				return err
			}
			for _, block := range inventory {
				if err := s.stageJournalBlockWithin(ctx, tx, Hash("local-snapshot:"+s.Lineage), block); err != nil {
					return err
				}
			}
			var pending []string
			for _, block := range blocks {
				if err := s.stageJournalBlockWithin(ctx, tx, Hash(scope.Endpoint), block); err != nil {
					return err
				}
				present, err := scope.HasBlock(ctx, block.Hash)
				if err != nil {
					return err
				}
				if !present {
					pending = append(pending, block.Hash)
					result.Blocks = append(result.Blocks, block)
				}
			}
			after, err := s.observationStampWithin(ctx, tx, a)
			if err != nil {
				if errors.Is(err, ErrObservationTracking) {
					return nil
				}
				return err
			}
			if wantSnapshot {
				result.snapshot = s.sealSemanticSnapshot(scope.Consumer, snapshot, after, inventory)
			}
			if advance && after.ArchiveEpoch == planned.ArchiveEpoch && after.Schema == planned.Schema {
				result.Observed = after
			}
			if len(pending) > maxPublicationPending {
				return nil
			}
			sort.Strings(pending)
			state := publicationJournal{Version: 1, Consumer: scope.Consumer, Endpoint: Hash(scope.Endpoint), Relay: scope.Relay, Observed: after, Tree: scope.Tree, Pending: pending}
			remembered, err := a.store().rememberWithin(ctx, tx, publicationJournalCursor(scope.Consumer), snapshot)
			if err != nil {
				return err
			}
			if !remembered {
				return errPublicationChanged
			}
			return s.savePublicationJournalWithin(ctx, tx, state)
		})
		if errors.Is(err, errPublicationChanged) {
			continue
		}
		if err != nil {
			return JournalPublicationPlan{}, err
		}
		if !wantSnapshot {
			result.Snapshot = nil
		}
		return result, nil
	}
	return JournalPublicationPlan{}, fmt.Errorf("archive changed repeatedly during complete publication materialization")
}

func (s Index) catalogBlockWithin(ctx context.Context, tx *sql.Tx, hash string) (Block, error) {
	if !hex64.MatchString(hash) {
		return Block{}, ErrIndexRebuild
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT index_version FROM history_index_blocks WHERE project_id=? AND block_hash=?`, s.ProjectID, hash).Scan(&version); err != nil || version != IndexVersion {
		return Block{}, ErrIndexRebuild
	}
	rows, err := tx.QueryContext(ctx, `SELECT substr(e.envelope_json,1,?) FROM history_index_block_members m LEFT JOIN history_index_envelopes e USING(project_id,envelope_hash) WHERE m.project_id=? AND m.block_hash=? ORDER BY e.envelope_json`, MaxObjectBytes+1, s.ProjectID, hash)
	if err != nil {
		return Block{}, err
	}
	var body bytes.Buffer
	body.WriteString(blockHeader)
	count := 0
	for rows.Next() {
		var item sql.NullString
		if err := rows.Scan(&item); err != nil {
			rows.Close()
			return Block{}, err
		}
		count++
		if !item.Valid || count > MaxBlockRecords || len(item.String) > MaxObjectBytes || body.Len()+len(item.String)+1 > MaxBlockBytes {
			rows.Close()
			return Block{}, ErrIndexRebuild
		}
		body.WriteString(item.String)
		body.WriteByte('\n')
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Block{}, err
	}
	block, err := DecodeBlock(s.Lineage, hash, body.Bytes())
	if err != nil {
		return Block{}, fmt.Errorf("%w: %v", ErrIndexRebuild, err)
	}
	// Callers hold a matching projection generation in this transaction. The
	// exact block and envelope bytes were just independently decoded and checked;
	// carry that verified source instead of rereading the same derived inventory.
	block.validated = sealBlockContents(s.Lineage, block.Objects)
	return block, nil
}

func (s Index) semanticSnapshotWithin(ctx context.Context, tx *sql.Tx) ([]Block, error) {
	rows, err := tx.QueryContext(ctx, `SELECT block_hash FROM history_index_publications WHERE project_id=? AND endpoint_hash=? ORDER BY block_hash`, s.ProjectID, Hash("local-snapshot:"+s.Lineage))
	if err != nil {
		return nil, err
	}
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return nil, err
		}
		hashes = append(hashes, hash)
		if len(hashes) > portable.MaxRecordCount {
			rows.Close()
			return nil, ErrIndexRebuild
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	blocks := make([]Block, 0, len(hashes))
	total := 0
	for _, hash := range hashes {
		block, err := s.catalogBlockWithin(ctx, tx, hash)
		if err != nil {
			return nil, err
		}
		total += len(block.Bytes) - len(blockHeader)
		if total > portable.MaxRecordsBytes {
			return nil, ErrIndexRebuild
		}
		blocks = append(blocks, block)
		syncwork.RecordWork(ctx, "semantic_catalog_blocks_read", 1)
		syncwork.RecordWork(ctx, "semantic_catalog_bytes_read", int64(len(block.Bytes)))
	}
	return blocks, nil
}

// AdvancePublicationObservation hands off only the exact before/after metadata
// mutation witnessed by observation. It may acknowledge local committed blocks,
// but never advances the archive-change cursor or claims remote delivery.
func (s Index) AdvancePublicationObservation(ctx context.Context, a Archive, scope JournalPublicationScope, mutation ObservationMutation) error {
	if err := s.validateJournalScope(a, scope); err != nil {
		return err
	}
	if !validObservationStamp(mutation.Before) || !validObservationStamp(mutation.After) {
		return nil
	}
	return a.store().transact(ctx, func(tx *sql.Tx) error {
		current, err := s.observationStampWithin(ctx, tx, a)
		if err != nil {
			if errors.Is(err, ErrObservationTracking) {
				return nil
			}
			return err
		}
		if current != mutation.After {
			return nil
		}
		state, known, err := s.loadPublicationJournalWithin(ctx, tx, scope)
		if err != nil {
			return err
		}
		if !known || state.Observed != mutation.Before {
			return nil
		}
		if state.Tree != scope.Tree {
			ok, err := scope.AppendOnly(ctx, state.Tree)
			if err != nil || !ok {
				return nil
			}
		}
		pending := make([]string, 0, len(state.Pending))
		for _, hash := range state.Pending {
			present, err := scope.HasBlock(ctx, hash)
			if err != nil {
				return err
			}
			if !present {
				pending = append(pending, hash)
			}
		}
		state.Pending, state.Tree, state.Observed = pending, scope.Tree, mutation.After
		return s.savePublicationJournalWithin(ctx, tx, state)
	})
}

// PublicationJournalStamp permits completion reuse only for a fully materialized
// archive with no outstanding local publication intent. Its digest is not a peer
// receipt and does not replace the transport's fetched-head/push checks.
func (s Index) PublicationJournalStamp(ctx context.Context, a Archive, scope JournalPublicationScope) (string, error) {
	if err := s.validateJournalScope(a, scope); err != nil {
		return "", err
	}
	var digest string
	err := a.store().view(ctx, func(tx *sql.Tx) error {
		current, err := s.observationStampWithin(ctx, tx, a)
		if err != nil {
			return err
		}
		state, known, err := s.loadPublicationJournalWithin(ctx, tx, scope)
		if err != nil {
			return err
		}
		if !known || len(state.Pending) != 0 || state.Observed != current {
			return errPublicationJournalFull
		}
		_, changes, fallback, err := a.store().changesWithin(ctx, tx, publicationJournalCursor(scope.Consumer), 1)
		if err != nil {
			return err
		}
		if fallback != "" || len(changes) != 0 {
			return errPublicationJournalFull
		}
		body, _ := json.Marshal(state)
		digest = Hash(string(body))
		return nil
	})
	return digest, err
}

// Drive from a bounded envelope membership range. An endpoint-first join lets
// SQLite scan every block at that endpoint, even for an absent new envelope.
const publicationMembershipQuery = `SELECT m.block_hash,
 EXISTS(SELECT 1 FROM history_index_publications p WHERE p.project_id=m.project_id AND p.endpoint_hash=? AND p.block_hash=m.block_hash),
 EXISTS(SELECT 1 FROM history_index_publications p WHERE p.project_id=m.project_id AND p.endpoint_hash=? AND p.block_hash=m.block_hash)
 FROM history_index_block_members m INDEXED BY history_index_envelope_members
 WHERE m.project_id=? AND m.envelope_hash=? ORDER BY m.block_hash LIMIT 257`

func (s Index) publicationMembershipWithin(ctx context.Context, tx *sql.Tx, hash, endpoint string) (bool, []string, error) {
	rows, err := tx.QueryContext(ctx, publicationMembershipQuery, Hash("local-snapshot:"+s.Lineage), Hash(endpoint), s.ProjectID, hash)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	semantic := false
	var candidates []string
	count := 0
	for rows.Next() {
		var block string
		var local, destination bool
		if err := rows.Scan(&block, &local, &destination); err != nil {
			return false, nil, err
		}
		count++
		syncwork.RecordWork(ctx, "publication_membership_rows_read", 1)
		if count > 256 {
			return false, nil, errPublicationJournalFull
		}
		semantic = semantic || local
		if destination {
			candidates = append(candidates, block)
		}
	}
	return semantic, candidates, rows.Err()
}
