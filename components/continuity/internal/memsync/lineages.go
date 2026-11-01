package memsync

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// Lineages lists canonical lineage partitions from the committed object tree,
// including unsupported version namespaces so setup cannot overlook existing
// histories. It does not interpret their records or scan loose files.
func Lineages(ctx context.Context, clone, remote string, access Access) ([]string, error) {
	repo, err := openBareRepository(ctx, clone, remote, access)
	if err != nil {
		return nil, err
	}
	lock, err := lockFile(filepath.Join(clone, "continuity-sync.lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	transport := &objectTransport{repo: repo}
	tree, err := transport.headTree(ctx)
	if err != nil || tree == "" {
		return nil, err
	}
	versions, err := transport.trees.read(ctx, repo, tree)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, version := range versions {
		if len(version.name) < 2 || version.name[0] != 'v' || version.name[1] < '0' || version.name[1] > '9' || version.kind != "tree" || version.mode != "40000" {
			continue
		}
		partitions, err := transport.trees.read(ctx, repo, version.oid)
		if err != nil {
			return nil, err
		}
		for _, partition := range partitions {
			lineage := "lineage:" + partition.name
			if _, err := recordsync.BlockDirectory(lineage); err == nil && partition.kind == "tree" && partition.mode == "40000" {
				seen[lineage] = true
			}
		}
	}
	lineages := make([]string, 0, len(seen))
	for lineage := range seen {
		lineages = append(lineages, lineage)
	}
	sort.Strings(lineages)
	return lineages, nil
}
