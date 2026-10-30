package memsync

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// blockTreeDiscovery caches immutable Git tree nodes for one locked cycle,
// optionally backed by local raw tree objects verified against their Git IDs.
// Tree identity proves membership only. Every returned path still goes through
// the ordinary filesystem and block validation, including on a cache hit.
// Persisted membership is never a receipt for semantic import.
type blockTreeDiscovery struct {
	nodes     map[string][]blockTreeEntry
	entries   int
	cache     *os.Root
	gitReads  int
	cacheHits int
}

type blockTreeEntry struct{ name, kind, oid string }

func (s Syncer) trackedBlockPaths(ctx context.Context, repo git, directory string) ([]string, error) {
	directory = filepath.ToSlash(directory)
	if s.discovery != nil {
		// write-tree describes the index, including staged additions/deletions.
		// HEAD alone would silently omit staged blocks used by conversion and
		// interrupted publication recovery. Unmerged/uncertain indexes fall back.
		if tree, err := repo.run(ctx, "write-tree"); err == nil {
			if paths, err := s.discovery.paths(ctx, repo, tree, directory); err == nil {
				return paths, nil
			}
		}
	}
	listing, err := repo.run(ctx, "ls-files", "-z", "--", directory)
	if err != nil {
		return nil, err
	}
	if listing == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00"), nil
}

func (d *blockTreeDiscovery) read(ctx context.Context, repo git, oid string) ([]blockTreeEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entries, ok := d.nodes[oid]; ok {
		return entries, nil
	}
	// Only object IDs emitted by Git enter this cache. No ref/path expression
	// from transport filenames is ever evaluated as a Git object selector.
	if len(oid) != 40 && len(oid) != 64 {
		return nil, fmt.Errorf("invalid tree identity")
	}
	for _, c := range oid {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return nil, fmt.Errorf("invalid tree identity")
		}
	}
	entries, err := d.treeEntries(ctx, repo, oid)
	if err != nil {
		return nil, err
	}
	d.entries += len(entries)
	if d.entries > recordsync.MaxScanEntries {
		return nil, fmt.Errorf("tree discovery exceeds bounds")
	}
	if d.nodes == nil {
		d.nodes = make(map[string][]blockTreeEntry)
	}
	d.nodes[oid] = entries
	return entries, nil
}

func (d *blockTreeDiscovery) paths(ctx context.Context, repo git, tree, directory string) ([]string, error) {
	for _, part := range strings.Split(directory, "/") {
		entries, err := d.read(ctx, repo, tree)
		if err != nil {
			return nil, err
		}
		tree = ""
		for _, entry := range entries {
			if entry.name == part {
				if entry.kind != "tree" {
					return nil, fmt.Errorf("block namespace is not a tree")
				}
				tree = entry.oid
				break
			}
		}
		if tree == "" {
			return nil, nil
		}
	}
	var paths []string
	visited := 0
	var walk func(string, string, int) error
	walk = func(oid, prefix string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("block tree nesting exceeds bounds")
		}
		entries, err := d.read(ctx, repo, oid)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			visited++
			if visited > recordsync.MaxScanEntries {
				return fmt.Errorf("block tree traversal exceeds bounds")
			}
			name := path.Join(prefix, entry.name)
			if entry.kind == "tree" {
				if err := walk(entry.oid, name, depth+1); err != nil {
					return err
				}
			} else {
				paths = append(paths, name)
				if len(paths) > recordsync.MaxScanEntries {
					return fmt.Errorf("block paths exceed bounds")
				}
			}
		}
		return nil
	}
	if err := walk(tree, directory, 0); err != nil {
		return nil, err
	}
	return paths, nil
}
