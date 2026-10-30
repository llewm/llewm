package recordsync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// ManifestCatalog is the union of the listed, durably announced snapshots.
// Roots and Blocks are sorted unique identities. It is not proof of endpoint
// completeness, block integrity, transport provenance or semantic import.
type ManifestCatalog struct {
	Roots, Blocks []string
	VisitedNodes  int
}

// DiscoverManifestCatalog selects every observed root, never a timestamp-based
// "latest" snapshot. Independent writers may announce disjoint inventories.
// Missing, damaged or changing metadata returns no catalog: callers must retain
// complete block discovery when catalog coverage cannot be established.
func DiscoverManifestCatalog(ctx context.Context, root *os.Root, lineage string) (ManifestCatalog, error) {
	roots, err := discoverManifestRoots(ctx, root, lineage)
	if err != nil {
		return ManifestCatalog{}, err
	}
	catalog, err := readManifestCatalog(ctx, root, lineage, roots)
	if err != nil {
		return ManifestCatalog{}, err
	}
	// A catalog describes only the selected snapshots. Refuse an observed
	// publication race rather than silently dropping a newly announced root.
	after, err := discoverManifestRoots(ctx, root, lineage)
	if err != nil {
		return ManifestCatalog{}, err
	}
	if !slices.Equal(roots, after) {
		return ManifestCatalog{}, fmt.Errorf("manifest roots changed during discovery")
	}
	return catalog, nil
}

func discoverManifestRoots(ctx context.Context, root *os.Root, lineage string) ([]string, error) {
	base, err := BlockDirectory(lineage)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(base), "manifest-roots")
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("manifest roots must be a real directory")
	}
	directory, err := openRead(root, path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	actual, err := directory.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, fmt.Errorf("manifest roots changed while opening")
	}
	var roots []string
	entries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		values, readErr := directory.ReadDir(256)
		for _, entry := range values {
			entries++
			if entries > MaxScanEntries {
				return nil, fmt.Errorf("manifest root entries exceed bounds")
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".llewm-stage-") {
				continue
			}
			hash := strings.TrimSuffix(name, ".root")
			if name != hash+".root" || !hex64.MatchString(hash) {
				return nil, fmt.Errorf("invalid manifest root entry")
			}
			roots = append(roots, hash)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no announced manifest roots")
	}
	sort.Strings(roots)
	return roots, nil
}

func readManifestCatalog(ctx context.Context, root *os.Root, lineage string, roots []string) (ManifestCatalog, error) {
	var catalog ManifestCatalog
	if len(roots) > MaxScanEntries {
		return catalog, fmt.Errorf("manifest root count exceeds bounds")
	}
	seen := make(map[string]string)
	blocks := make(map[string]bool)
	var walk func(string, string) error
	walk = func(hash, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if prior, ok := seen[hash]; ok {
			if prior != prefix {
				return fmt.Errorf("shared manifest has conflicting radix positions")
			}
			return nil
		}
		catalog.VisitedNodes++
		if catalog.VisitedNodes > MaxScanEntries {
			return fmt.Errorf("manifest traversal exceeds bounds")
		}
		path, err := manifestPath(lineage, hash)
		if err != nil {
			return err
		}
		body, err := ReadFile(root, path, MaxManifestBytes)
		if err != nil {
			return err
		}
		m, err := DecodeManifest(lineage, hash, prefix, body)
		if err != nil {
			return err
		}
		seen[hash] = prefix
		for _, hash := range m.Blocks {
			blocks[hash] = true
		}
		if len(blocks) > portable.MaxRecordCount {
			return fmt.Errorf("manifest blocks exceed bounds")
		}
		for _, digit := range "0123456789abcdef" {
			child := prefix + string(digit)
			if hash, ok := m.Children[child]; ok {
				if err := walk(hash, child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, hash := range roots {
		if err := ctx.Err(); err != nil {
			return ManifestCatalog{}, err
		}
		if err := readManifestAnnouncement(root, lineage, hash); err != nil {
			return ManifestCatalog{}, err
		}
		if err := walk(hash, ""); err != nil {
			return ManifestCatalog{}, err
		}
	}
	catalog.Roots = append([]string(nil), roots...)
	for hash := range blocks {
		catalog.Blocks = append(catalog.Blocks, hash)
	}
	sort.Strings(catalog.Blocks)
	return catalog, nil
}
