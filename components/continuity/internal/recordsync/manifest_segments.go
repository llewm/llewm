package recordsync

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

type manifestPublicationBase struct {
	root    *os.Root
	lineage string
	blocks  []string
}

// PublishManifestSegments catalogs blocks published by the caller in bounded,
// immutable arrival batches. Existing catalog membership is never repacked:
// backdated arrivals append now, independently of record timestamps. Segment
// roots have no global ordering or last-writer semantics; readers union them.
// A scan's private catalog can be reused only with the same open root/lineage.
// Without one, rediscover all roots; uncertain metadata catalogs the complete
// supplied inventory instead. Neither path removes history or proves coverage.
func (scan BlockScanResult) PublishManifestSegments(ctx context.Context, root *os.Root, lineage string, hashes []string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !lineagePattern.MatchString(lineage) || len(hashes) > portable.MaxRecordCount || scan.Pending+scan.Invalid != 0 {
		return nil, fmt.Errorf("manifest segments require a valid complete inventory")
	}
	values := append([]string(nil), hashes...)
	sort.Strings(values)
	for i, hash := range values {
		if !hex64.MatchString(hash) || (i > 0 && hash == values[i-1]) {
			return nil, fmt.Errorf("invalid or duplicate segment block")
		}
	}
	base := scan.publicationBase
	if base == nil || base.root != root || base.lineage != lineage {
		catalog, err := DiscoverManifestCatalog(ctx, root, lineage)
		if err == nil {
			base = &manifestPublicationBase{root: root, lineage: lineage, blocks: catalog.Blocks}
		} else {
			base = nil
		}
	}
	covered := make(map[string]bool)
	if base != nil {
		for _, hash := range base.blocks {
			covered[hash] = true
		}
	}
	var added []string
	for _, hash := range values {
		if !covered[hash] {
			added = append(added, hash)
		}
	}
	var roots []string
	// Keep each arrival segment bounded even on initial publication. A crash
	// may announce a prefix; the next complete scan discovers it and publishes
	// the remainder. Concurrent writers may overlap without losing membership.
	for start := 0; start < len(added); start += MaxManifestEntries {
		end := min(start+MaxManifestEntries, len(added))
		hash, _, err := PublishManifest(ctx, root, lineage, added[start:end])
		if err != nil {
			return roots, err
		}
		roots = append(roots, hash)
	}
	if len(values) == 0 && base == nil {
		hash, _, err := PublishManifest(ctx, root, lineage, nil)
		if err != nil {
			return roots, err
		}
		roots = append(roots, hash)
	}
	return roots, ctx.Err()
}
