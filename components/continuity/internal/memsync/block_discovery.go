package memsync

import (
	"context"
	"fmt"
	"os"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// blockTreeDiscovery caches hash-verified immutable tree nodes for one locked
// cycle, optionally backed by disposable raw objects. It proves membership,
// never envelope validity, provenance authorization or semantic import.
type blockTreeDiscovery struct {
	nodes     map[string][]blockTreeEntry
	entries   int
	cache     *os.Root
	gitReads  int
	cacheHits int
}

type blockTreeEntry struct{ name, kind, oid, mode string }

func (d *blockTreeDiscovery) read(ctx context.Context, repo git, oid string) ([]blockTreeEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entries, ok := d.nodes[oid]; ok {
		return entries, nil
	}
	if !validGitOID(oid) {
		return nil, fmt.Errorf("invalid tree identity")
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
