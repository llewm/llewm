package foldersync

import (
	"context"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// Inventory reads a complete endpoint snapshot without relying on the derived
// block index. It is suitable for rebuilding that index only after every
// configured endpoint has supplied an equally complete snapshot.
func (s Syncer) Inventory(ctx context.Context) ([]recordsync.Block, error) {
	root, err := recordsync.OpenRoot(s.Endpoint.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	marker, err := readMarker(root)
	if err != nil {
		return nil, err
	}
	if marker.StoreID != s.Endpoint.StoreID || s.Endpoint.LineageID != s.Archive.Lineage {
		return nil, fmt.Errorf("folder identity/lineage differs from explicit configuration")
	}
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.LineageID != s.Endpoint.LineageID {
		return nil, fmt.Errorf("folder route differs from selected project's lineage")
	}
	scan, err := recordsync.ScanBlocks(ctx, root, s.Archive.Lineage)
	if err != nil {
		return nil, err
	}
	if scan.Pending+scan.Invalid != 0 {
		return nil, fmt.Errorf("folder block inventory is incomplete or invalid")
	}
	if err := verifyConversion(root, marker, s.Archive.Lineage, scan.Blocks); err != nil {
		return nil, err
	}
	live, liveErr := Inspect(s.Endpoint.Directory)
	current, currentErr := readMarker(root)
	if liveErr != nil || live != marker || currentErr != nil || current != marker {
		return nil, fmt.Errorf("configured folder path or marker became unavailable or changed")
	}
	return scan.Blocks, nil
}

// Sync performs one v4 folder cycle. The caller captures local records into
// Archive first and explicitly converts each lineage before its first sync.
func (s Syncer) Sync(ctx context.Context) (result Result, err error) {
	result = Result{Endpoint: s.Endpoint.Name, Outcome: "error", At: time.Now().UTC(), PeerReceipt: "unknown"}
	defer func() {
		if err != nil {
			result.Detail = redact.String(err.Error())
		}
		if saveErr := s.Archive.SaveStatus(s.Endpoint.ID(), result); err == nil && saveErr != nil {
			err = saveErr
		}
	}()
	root, err := recordsync.OpenRoot(s.Endpoint.Directory)
	if err != nil {
		result.Outcome = "unavailable"
		return result, err
	}
	defer root.Close()
	marker, err := readMarker(root)
	if err != nil {
		result.Outcome = "unavailable"
		return result, err
	}
	if marker.StoreID != s.Endpoint.StoreID || s.Endpoint.LineageID != s.Archive.Lineage {
		return result, fmt.Errorf("folder identity/lineage differs from explicit configuration")
	}
	identity, err := (portable.Service{DB: s.DB}).Get(ctx, s.ProjectID)
	if err != nil {
		return result, err
	}
	if identity == nil || identity.LineageID != s.Endpoint.LineageID {
		return result, fmt.Errorf("folder route differs from selected project's lineage")
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Archive.Lineage}
	scan, err := index.ScanBlocks(ctx, root)
	if err != nil {
		return result, err
	}
	result.PendingFiles, result.InvalidFiles = scan.Pending, scan.Invalid
	if scan.Pending+scan.Invalid != 0 {
		result.Outcome = "pending"
		return result, fmt.Errorf("folder block inventory is incomplete or invalid")
	}
	if err := verifyConversion(root, marker, s.Archive.Lineage, scan.Blocks); err != nil {
		result.Outcome = "pending"
		return result, err
	}
	for _, block := range scan.Blocks {
		added, err := index.ObservePublication(ctx, s.Archive, s.Endpoint.ID(), block)
		result.ObservedRecords += added
		if err != nil {
			return result, err
		}
	}
	blocks, all, err := index.PlanPublicationWithInventory(ctx, s.Archive, s.Endpoint.ID(), s.Endpoint.Relay)
	if err != nil {
		return result, err
	}
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		live, liveErr := Inspect(s.Endpoint.Directory)
		current, currentErr := readMarker(root)
		if liveErr != nil || live != marker || currentErr != nil || current != marker {
			return result, fmt.Errorf("configured folder path or marker became unavailable or changed")
		}
		added, err := recordsync.PublishBlock(root, s.Archive.Lineage, block)
		if err != nil {
			return result, err
		}
		if added {
			result.PublishedRecords += len(block.Objects)
		}
	}
	result.ImportResult, err = index.ImportBlocks(ctx, all)
	if err != nil {
		result.Outcome = "pending"
		return result, err
	}
	result.Outcome = "published_locally"
	result.Detail = "validated locally; external delivery and peer receipt are unconfirmed"
	if result.DeferredRecords+result.Collisions > 0 {
		result.Outcome = "pending"
	}
	return result, nil
}
