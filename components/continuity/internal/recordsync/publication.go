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

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

var errPublicationChanged = errors.New("publication membership changed during archive scan")

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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=index_version WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return 0, err
	}
	var stale int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=? AND block_hash=? AND index_version<>?`, s.ProjectID, block.Hash, IndexVersion).Scan(&stale); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_index_publications(project_id,endpoint_hash,block_hash) VALUES(?,?,?) ON CONFLICT DO NOTHING`, s.ProjectID, Hash(endpoint), block.Hash); err != nil {
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
	all, err := packValidatedBlocks(s.Lineage, inventory)
	return blocks, all, err
}

func (s Index) planPublicationWithRecovery(ctx context.Context, archive Archive, endpoint string, relay bool, inventory *[]portable.EncodedEnvelope, known []Block) ([]Block, error) {
	repaired := false
	for attempt := 0; attempt < 3; attempt++ {
		blocks, err := s.planPublication(ctx, archive, endpoint, relay, inventory, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
			return archive.objectsWithBlocks(ctx, endpoint, relay, known)
		})
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
	rows, err := tx.QueryContext(ctx, `SELECT p.block_hash,r.envelope_hash,r.envelope_json FROM history_index_publications p LEFT JOIN history_index_records r ON r.project_id=p.project_id AND r.block_hash=p.block_hash WHERE p.project_id=? AND p.endpoint_hash=? ORDER BY p.block_hash,r.envelope_json`, s.ProjectID, endpointHash)
	if err != nil {
		return nil, err
	}
	type saved struct {
		hash  string
		items []struct{ hash, body string }
	}
	var savedBlocks []saved
	rowCount, rowBytes := 0, 0
	for rows.Next() {
		rowCount++
		if rowCount > portable.MaxRecordCount {
			rows.Close()
			return nil, fmt.Errorf("%w: publication record count exceeds bounds", ErrIndexRebuild)
		}
		var hash string
		var objectHash, body sql.NullString
		if err := rows.Scan(&hash, &objectHash, &body); err != nil {
			rows.Close()
			return nil, err
		}
		if len(savedBlocks) == 0 || savedBlocks[len(savedBlocks)-1].hash != hash {
			savedBlocks = append(savedBlocks, saved{hash: hash})
			if len(savedBlocks) > portable.MaxRecordCount {
				rows.Close()
				return nil, fmt.Errorf("%w: too many publication blocks", ErrIndexRebuild)
			}
		}
		if !body.Valid || !objectHash.Valid {
			rows.Close()
			return nil, fmt.Errorf("%w: publication has no records", ErrIndexRebuild)
		}
		rowBytes += len(body.String) + 1
		if rowBytes > portable.MaxRecordsBytes {
			rows.Close()
			return nil, fmt.Errorf("%w: publication records exceed bounds", ErrIndexRebuild)
		}
		savedBlocks[len(savedBlocks)-1].items = append(savedBlocks[len(savedBlocks)-1].items, struct{ hash, body string }{objectHash.String, body.String})
		if len(savedBlocks[len(savedBlocks)-1].items) > MaxBlockRecords || len(body.String) > MaxObjectBytes {
			rows.Close()
			return nil, fmt.Errorf("%w: publication exceeds bounds", ErrIndexRebuild)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool, len(allowed))
	var result []Block
	for _, saved := range savedBlocks {
		var body bytes.Buffer
		body.WriteString(blockHeader)
		permitted, addsRecord := true, false
		var objects []portable.EncodedEnvelope
		for _, item := range saved.items {
			if body.Len()+len(item.body)+1 > MaxBlockBytes {
				return nil, fmt.Errorf("%w: publication exceeds bounds", ErrIndexRebuild)
			}
			body.WriteString(item.body)
			body.WriteByte('\n')
			var envelope portable.Envelope
			if err := json.Unmarshal([]byte(item.body), &envelope); err != nil || envelope.EnvelopeSHA256 != item.hash || envelope.LineageID != s.Lineage {
				return nil, fmt.Errorf("%w: invalid publication entry", ErrIndexRebuild)
			}
			existing, ok := byHash[item.hash]
			if !ok || !bytes.Equal(existing.Bytes, []byte(item.body)) {
				permitted = false
				continue
			}
			objects = append(objects, existing)
			if !covered[item.hash] {
				addsRecord = true
			}
		}
		sum := sha256.Sum256(body.Bytes())
		if len(saved.items) == 0 || len(saved.items) > MaxBlockRecords || body.Len() > MaxBlockBytes || hex.EncodeToString(sum[:]) != saved.hash {
			return nil, fmt.Errorf("%w: invalid publication block", ErrIndexRebuild)
		}
		if permitted && addsRecord {
			block := Block{Hash: saved.hash, Bytes: append([]byte(nil), body.Bytes()...), Objects: objects}
			result = append(result, block)
			for _, object := range block.Objects {
				covered[object.Envelope.EnvelopeSHA256] = true
			}
		}
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
