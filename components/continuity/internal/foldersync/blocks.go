package foldersync

import (
	"context"
	"fmt"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// syncBlocks performs one v4 folder cycle. The caller must capture local
// records into Archive before invoking it, and perform explicit v3 conversion
// before switching a live endpoint to this path.
func (s Syncer) syncBlocks(ctx context.Context) (result Result, err error) {
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
	scan, err := recordsync.ScanBlocks(ctx, root, s.Archive.Lineage)
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
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: s.Archive.Lineage}
	for _, block := range scan.Blocks {
		for _, object := range block.Objects {
			added, err := s.Archive.Put(object, s.Endpoint.ID())
			if err != nil {
				return result, err
			}
			if added {
				result.ObservedRecords++
			}
		}
		if err := index.ObservePublication(ctx, s.Archive, s.Endpoint.ID(), block); err != nil {
			return result, err
		}
	}
	blocks, err := index.PlanPublication(ctx, s.Archive, s.Endpoint.ID(), s.Endpoint.Relay)
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
	all, err := s.Archive.AuthorizedBlocks(ctx, "", true)
	if err != nil {
		return result, err
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
