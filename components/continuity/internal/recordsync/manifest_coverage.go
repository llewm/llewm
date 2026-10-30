package recordsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// manifestCoverage is disposable local evidence of directory membership, not
// block integrity, semantic import or provider delivery. The directory identity
// includes change time: adding, removing or renaming any entry invalidates it.
// File contents are deliberately not covered; every block is still read.
type manifestCoverage struct {
	Version   int       `json:"version"`
	Catalog   string    `json:"catalog"`
	Directory [8]uint64 `json:"directory"`
}

func blockDirectoryIdentity(root *os.Root, lineage string) ([8]uint64, error) {
	path, err := BlockDirectory(lineage)
	if err != nil {
		return [8]uint64{}, err
	}
	info, err := root.Lstat(path)
	if err != nil {
		return [8]uint64{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return [8]uint64{}, fmt.Errorf("block directory must be a real directory")
	}
	directory, err := openRead(root, path)
	if err != nil {
		return [8]uint64{}, err
	}
	defer directory.Close()
	actual, err := directory.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return [8]uint64{}, fmt.Errorf("block directory changed while opening")
	}
	identity, ok := fileIdentity(actual)
	if !ok {
		return [8]uint64{}, fmt.Errorf("block directory change identity unavailable")
	}
	return identity, nil
}

// ScanBlocksWithCatalog reuses catalog membership only after a complete valid
// scan matched it under a stable local block-directory identity. Receipts live
// in the local archive, scoped to this project, lineage and endpoint. They are
// never transported. Unknown, damaged, changed or unsupported state falls back
// to the ordinary complete scan. Inventory rebuilds must still use ScanBlocks.
//
// This saves directory enumeration only. Manifest traversal and all block byte
// and index validation remain; it makes no end-to-end scaling claim.
func (s Index) ScanBlocksWithCatalog(ctx context.Context, root *os.Root, archive Archive, endpoint string) (BlockScanResult, error) {
	if err := ctx.Err(); err != nil {
		return BlockScanResult{}, err
	}
	if archive.Lineage != s.Lineage || endpoint == "" {
		return s.ScanBlocks(ctx, root)
	}
	before, guardErr := blockDirectoryIdentity(root, s.Lineage)
	if guardErr != nil {
		return s.ScanBlocks(ctx, root)
	}
	catalog, catalogErr := DiscoverManifestCatalog(ctx, root, s.Lineage)
	if catalogErr != nil {
		return s.ScanBlocks(ctx, root)
	}
	key := "manifest-coverage:" + Hash(s.ProjectID+"\x00"+s.Lineage+"\x00"+endpoint)
	expected := manifestCoverage{Version: 1, Catalog: Hash(s.Lineage + "\n" + strings.Join(catalog.Roots, "\n")), Directory: before}
	var saved manifestCoverage
	if archive.Status(key, &saved) == nil && saved == expected {
		result, err := s.scanCatalogBlocks(ctx, root, catalog.Blocks)
		after, guardErr := blockDirectoryIdentity(root, s.Lineage)
		if guardErr == nil && before == after {
			result.CatalogReused = true
			return result, err
		}
		// A concurrent arrival may not have a manifest yet. Discard the
		// partial snapshot and enumerate the authoritative directory again.
		return s.ScanBlocks(ctx, root)
	}
	result, err := s.ScanBlocks(ctx, root)
	if err != nil || result.Pending+result.Invalid != 0 {
		return result, err
	}
	hashes := make([]string, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		hashes = append(hashes, block.Hash)
	}
	after, guardErr := blockDirectoryIdentity(root, s.Lineage)
	if guardErr == nil && before == after && slices.Equal(hashes, catalog.Blocks) {
		// A failed cache write must not turn a successful complete scan into
		// an error. The next cycle simply takes the full path again.
		_ = archive.SaveStatus(key, expected)
	}
	return result, nil
}

func (s Index) scanCatalogBlocks(ctx context.Context, root *os.Root, hashes []string) (BlockScanResult, error) {
	var result BlockScanResult
	totalBytes, totalRecords := 0, 0
	for _, hash := range hashes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path, err := blockPath(s.Lineage, hash)
		if err != nil {
			return result, err
		}
		block, err := s.ReadBlock(ctx, root, path)
		if err != nil {
			if errors.Is(err, ErrIndexRebuild) {
				return result, err
			}
			result.Pending++
			continue
		}
		totalBytes += len(block.Bytes) - len(blockHeader)
		totalRecords += len(block.Objects)
		if totalBytes > portable.MaxRecordsBytes || totalRecords > portable.MaxRecordCount {
			return result, fmt.Errorf("block scan records exceed bounds")
		}
		result.Blocks = append(result.Blocks, block)
	}
	return result, ctx.Err()
}
