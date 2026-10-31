package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

var errPublicationChanged = errors.New("publication membership changed during archive scan")

// ReadPublication verifies a destination block and its inventory once, then
// records its archive provenance and membership before returning the snapshot.
// The objects are handed directly from ReadBlock, never from caller-owned
// parsed fields or a snapshot retained across a later inventory mutation.
func (s Index) ReadPublication(ctx context.Context, root *os.Root, path string, archive Archive, endpoint string) (Block, error) {
	if endpoint == "" || archive.Lineage != s.Lineage {
		return Block{}, fmt.Errorf("publication endpoint and matching lineage are required")
	}
	block, err := s.ReadBlock(ctx, root, path)
	if err != nil {
		return Block{}, err
	}
	if _, err := s.observePublicationObjects(ctx, archive, endpoint, block.Hash, block.Objects); err != nil {
		return Block{}, err
	}
	return block, nil
}

type publicationReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// publicationFingerprint detects a concurrent plan or rebuild between the
// archive snapshot and the writer transaction. Membership is append-only in a
// plan; a rebuild may replace it, so hash the entire ordered set.
func (s Index) publicationFingerprint(ctx context.Context, reader publicationReader, endpoint string) (string, error) {
	rows, err := reader.QueryContext(ctx, `SELECT block_hash FROM history_index_publications WHERE project_id=? AND endpoint_hash=? ORDER BY block_hash`, s.ProjectID, Hash(endpoint))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	count := 0
	for rows.Next() {
		var block string
		if err := rows.Scan(&block); err != nil {
			return "", err
		}
		count++
		if count > portable.MaxRecordCount {
			return "", fmt.Errorf("publication membership exceeds bounds")
		}
		h.Write([]byte(block))
		h.Write([]byte{0})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ObservePublication validates the derived inventory, records each envelope's
// destination provenance, and then records publication membership. Inventory
// rows are not semantic import receipts; callers import only after this succeeds.
func (s Index) ObservePublication(ctx context.Context, archive Archive, endpoint string, block Block) (int, error) {
	if endpoint == "" || archive.Lineage != s.Lineage {
		return 0, fmt.Errorf("publication endpoint and matching lineage are required")
	}
	if err := validateBlockBytes(block); err != nil || block.Hash == "" {
		return 0, fmt.Errorf("invalid observed block")
	}
	objects, err := s.blockObjects(ctx, []Block{block})
	if errors.Is(err, ErrIndexRebuild) {
		if repairErr := s.RepairBlock(ctx, block); repairErr != nil {
			return 0, repairErr
		}
		objects, err = s.blockObjects(ctx, []Block{block})
	}
	if err != nil {
		return 0, err
	}
	return s.observePublicationObjects(ctx, archive, endpoint, block.Hash, objects)
}

// Both callers supply objects just reconstructed from the exact block bytes
// and checked against the derived inventory. Keep the membership writer lock
// and stale-version guard shared with the standalone observation path.
func (s Index) observePublicationObjects(ctx context.Context, archive Archive, endpoint, hash string, objects []portable.EncodedEnvelope) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return 0, err
	}
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND block_hash=? AND index_version<>?`, s.ProjectID, hash, IndexVersion).Scan(&stale); err != nil {
		return 0, err
	}
	if stale != 0 {
		return 0, fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	root, err := OpenRoot(archive.Directory)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	addedCount := 0
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return addedCount, err
		}
		added, err := putValidatedRoot(root, object, endpoint)
		if err != nil {
			return addedCount, err
		}
		if added {
			addedCount++
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, Hash(endpoint), hash); err != nil {
		return addedCount, err
	}
	return addedCount, tx.Commit()
}

// PlanPublication stages immutable publication intent before returning blocks
// for delivery. Callers publish every returned block: this also repairs a
// block missing from the destination after an interrupted external sync.
// The index is derived; after clearing it, rebuild publication intent from
// verified destination blocks before planning additions. An incomplete or
// invalid destination scan must be retried before rebuilding its catalog.
func (s Index) PlanPublication(ctx context.Context, archive Archive, endpoint string, relay bool) ([]Block, error) {
	return s.planPublicationWithRecovery(ctx, archive, endpoint, relay, nil, nil)
}

// PlanPublicationWithInventory returns the publication plan and a complete
// validated archive snapshot from the same scan, for local semantic import.
// Records arriving after the snapshot are picked up on the next cycle.
func (s Index) PlanPublicationWithInventory(ctx context.Context, archive Archive, endpoint string, relay bool, known []Block) ([]Block, []Block, error) {
	var inventory []portable.EncodedEnvelope
	blocks, err := s.planPublicationWithRecovery(ctx, archive, endpoint, relay, &inventory, known)
	if err != nil {
		return nil, nil, err
	}
	// The publication plan already contains stable, validated blocks whose
	// objects are in this same archive snapshot. Preserve those identities for
	// import instead of repacking old history whenever one envelope arrives.
	covered := make(map[string]bool, len(inventory))
	all := append([]Block(nil), blocks...)
	for _, block := range blocks {
		for _, object := range block.Objects {
			covered[object.Envelope.EnvelopeSHA256] = true
		}
	}
	var remaining []portable.EncodedEnvelope
	for _, object := range inventory {
		if !covered[object.Envelope.EnvelopeSHA256] {
			remaining = append(remaining, object)
		}
	}
	if len(remaining) == 0 {
		return blocks, all, nil
	}
	// Keep residual membership in the same derived catalog, under a local-only
	// namespace that is never used as transport provenance. A foreign arrival
	// must not repack every previous foreign record into another synthetic block.
	// The scan already validated these exact archive bytes. Reuse that snapshot;
	// the planner checks stored membership against it and packs only additions.
	extra, err := s.planPublicationWithScanRecovery(ctx, archive, "local-snapshot:"+s.Lineage, true, nil, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
		return remaining, remaining, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return blocks, append(all, extra...), nil
}

func (s Index) planPublicationWithRecovery(ctx context.Context, archive Archive, endpoint string, relay bool, inventory *[]portable.EncodedEnvelope, known []Block) ([]Block, error) {
	return s.planPublicationWithScanRecovery(ctx, archive, endpoint, relay, inventory, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
		return archive.objectsWithBlocks(ctx, endpoint, relay, known)
	})
}

func (s Index) planPublicationWithScanRecovery(ctx context.Context, archive Archive, endpoint string, relay bool, inventory *[]portable.EncodedEnvelope, scan func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error)) ([]Block, error) {
	repaired := false
	for attempt := 0; attempt < 3; attempt++ {
		blocks, err := s.planPublication(ctx, archive, endpoint, relay, inventory, scan)
		if errors.Is(err, errPublicationChanged) {
			continue
		}
		if !errors.Is(err, ErrIndexRebuild) || repaired {
			return blocks, err
		}
		if repairErr := s.repairStored(ctx, endpoint); repairErr != nil {
			// The archive retains exact validated observations even when a cached
			// body was damaged. Rebuild only the already-staged endpoint blocks;
			// normal publication planning below still enforces origin authorization.
			_, objects, archiveErr := archive.objects(ctx, endpoint, true)
			if archiveErr != nil {
				return nil, errors.Join(repairErr, archiveErr)
			}
			byHash := make(map[string]portable.EncodedEnvelope, len(objects))
			for _, object := range objects {
				byHash[object.Envelope.EnvelopeSHA256] = object
			}
			if archiveErr = s.repairStoredWithArchive(ctx, endpoint, byHash); archiveErr != nil {
				return nil, errors.Join(repairErr, archiveErr)
			}
		}
		repaired = true
	}
	return nil, fmt.Errorf("publication changed repeatedly: %w", errPublicationChanged)
}

func (s Index) planPublication(ctx context.Context, archive Archive, endpoint string, relay bool, inventory *[]portable.EncodedEnvelope, scan func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error)) ([]Block, error) {
	if endpoint == "" || archive.Lineage != s.Lineage {
		return nil, fmt.Errorf("publication endpoint and matching lineage are required")
	}
	before, err := s.publicationFingerprint(ctx, s.DB, endpoint)
	if err != nil {
		return nil, err
	}
	allowed, all, err := scan()
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Reject a snapshot taken before a concurrent plan or rebuild changed the
	// endpoint membership, and retry outside the writer transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return nil, err
	}
	after, err := s.publicationFingerprint(ctx, tx, endpoint)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, errPublicationChanged
	}
	byHash := make(map[string]portable.EncodedEnvelope, len(allowed))
	for _, object := range allowed {
		byHash[object.Envelope.EnvelopeSHA256] = object
	}
	endpointHash := Hash(endpoint)
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications p JOIN history_index_blocks b ON b.project_id=p.project_id AND b.block_hash=p.block_hash WHERE p.project_id=? AND p.endpoint_hash=? AND b.index_version<>?`, s.ProjectID, endpointHash, IndexVersion).Scan(&stale); err != nil {
		return nil, err
	}
	if stale != 0 {
		return nil, fmt.Errorf("%w: version changed", ErrIndexRebuild)
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.block_hash,r.envelope_hash,r.envelope_json FROM history_index_publications p LEFT JOIN history_index_block_members m ON m.project_id=p.project_id AND m.block_hash=p.block_hash LEFT JOIN history_index_envelopes r ON r.project_id=m.project_id AND r.envelope_hash=m.envelope_hash WHERE p.project_id=? AND p.endpoint_hash=? ORDER BY p.block_hash,r.envelope_json`, s.ProjectID, endpointHash)
	if err != nil {
		return nil, err
	}
	// Consume the ordered catalog one block at a time. Retaining all SQL bodies
	// duplicates the entire archive snapshot until validation finishes.
	defer rows.Close()
	covered := make(map[string]bool, len(allowed))
	var result []Block
	var hash string
	var body bytes.Buffer
	var objects []portable.EncodedEnvelope
	permitted, addsRecord := true, false
	blockRecords := 0
	finish := func() error {
		if blockRecords == 0 {
			return nil
		}
		sum := sha256.Sum256(body.Bytes())
		if hex.EncodeToString(sum[:]) != hash {
			return fmt.Errorf("%w: invalid publication block", ErrIndexRebuild)
		}
		if permitted && addsRecord {
			block := Block{Hash: hash, Bytes: append([]byte(nil), body.Bytes()...), Objects: objects}
			block.validated = sealBlockContents(s.Lineage, objects)
			result = append(result, block)
			for _, object := range objects {
				covered[object.Envelope.EnvelopeSHA256] = true
			}
		}
		return nil
	}
	rowCount, rowBytes := 0, 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rowCount++
		if rowCount > portable.MaxRecordCount {
			return nil, fmt.Errorf("%w: publication record count exceeds bounds", ErrIndexRebuild)
		}
		var nextHash string
		var objectHash, item sql.NullString
		if err := rows.Scan(&nextHash, &objectHash, &item); err != nil {
			return nil, err
		}
		if blockRecords == 0 || nextHash != hash {
			if err := finish(); err != nil {
				return nil, err
			}
			hash = nextHash
			body.Reset()
			body.WriteString(blockHeader)
			objects = nil
			permitted, addsRecord = true, false
			blockRecords = 0
		}
		if !item.Valid || !objectHash.Valid {
			return nil, fmt.Errorf("%w: publication has no records", ErrIndexRebuild)
		}
		rowBytes += len(item.String) + 1
		if rowBytes > portable.MaxRecordsBytes {
			return nil, fmt.Errorf("%w: publication records exceed bounds", ErrIndexRebuild)
		}
		blockRecords++
		if blockRecords > MaxBlockRecords || len(item.String) > MaxObjectBytes || body.Len()+len(item.String)+1 > MaxBlockBytes {
			return nil, fmt.Errorf("%w: publication exceeds bounds", ErrIndexRebuild)
		}
		body.WriteString(item.String)
		body.WriteByte('\n')
		existing, ok := byHash[objectHash.String]
		if !ok || !bytes.Equal(existing.Bytes, []byte(item.String)) {
			// Entries outside the validated, permitted archive snapshot still
			// need their own identity check, even when not being published.
			syncwork.RecordWork(ctx, "publication_envelope_decodes", 1)
			var envelope portable.Envelope
			if err := json.Unmarshal([]byte(item.String), &envelope); err != nil || envelope.EnvelopeSHA256 != objectHash.String || envelope.LineageID != s.Lineage {
				return nil, fmt.Errorf("%w: invalid publication entry", ErrIndexRebuild)
			}
			permitted = false
			continue
		}
		// Exact bytes from this scan already carry a validated identity.
		// Reuse its parsed envelope; finish still checks complete membership.
		syncwork.RecordWork(ctx, "publication_envelopes_reused", 1)
		objects = append(objects, existing)
		if !covered[objectHash.String] {
			addsRecord = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := finish(); err != nil {
		return nil, err
	}
	var missing []portable.EncodedEnvelope
	for _, object := range allowed {
		if !covered[object.Envelope.EnvelopeSHA256] {
			missing = append(missing, object)
		}
	}
	newBlocks, err := packValidatedBlocks(s.Lineage, missing)
	if err != nil {
		return nil, err
	}
	for _, block := range newBlocks {
		if err := s.addBlockWithin(ctx, tx, block); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, endpointHash, block.Hash); err != nil {
			return nil, err
		}
		block.validated = sealBlockContents(s.Lineage, block.Objects)
		result = append(result, block)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if inventory != nil {
		*inventory = all
	}
	return result, nil
}
