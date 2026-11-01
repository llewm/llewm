package memsync

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// convertLegacy validates and archives all selected format2/3/4 observations,
// then replaces only those namespaces in one parent-preserving commit. The
// caller must stop old writers and hold the clone lock. Old bytes remain in
// Git history; no worktree or index participates in conversion.
func (s Syncer) convertLegacy(ctx context.Context, repo git, identity portable.Identity) (int, error) {
	if s.Archive == nil || s.Archive.DB != s.DB || s.Archive.ProjectID != s.ProjectID || s.Archive.Lineage != identity.LineageID {
		return 0, fmt.Errorf("matching history archive is required for conversion")
	}
	transport := s.objectReader(repo)
	branch, err := transport.currentBranch(ctx)
	if err != nil || branch.OID == "" {
		return 0, err
	}
	commit, err := transport.readCommit(ctx, branch.OID)
	if err != nil {
		return 0, err
	}
	if err := transport.validateBlockLayoutForConversion(ctx, commit.Tree, identity.LineageID, true); err != nil {
		return 0, err
	}
	partition := strings.TrimPrefix(identity.LineageID, "lineage:")
	var namespaces []string
	for _, version := range []string{"v2", "v3", "v4"} {
		selected, err := transport.treeAt(ctx, commit.Tree, version, partition)
		if err != nil {
			return 0, err
		}
		if selected != "" {
			namespaces = append(namespaces, version+"/"+partition)
		}
	}
	if len(namespaces) == 0 {
		return 0, nil
	}
	var scan recordsync.ScanResult
	seen := map[string][]byte{}
	for _, previous := range []bool{true, false} {
		legacy, err := transport.legacySnapshot(ctx, commit.Tree, identity.LineageID, previous)
		if err != nil {
			return 0, err
		}
		for _, object := range legacy.Objects {
			hash := object.Envelope.EnvelopeSHA256
			if known, ok := seen[hash]; ok {
				if !bytes.Equal(known, object.Bytes) {
					return 0, fmt.Errorf("legacy observation identity collision")
				}
				continue
			}
			seen[hash] = object.Bytes
			scan.Objects = append(scan.Objects, object)
		}
	}
	blocks, err := recordsync.ConvertV3Snapshot(identity.LineageID, scan)
	if err != nil {
		return 0, err
	}
	// Format4 already contains exact block frames. Relocate their blobs rather
	// than repacking envelopes, including on a resumed partial conversion.
	leaves, err := transport.conversionBlockLeaves(ctx, commit.Tree, identity.LineageID)
	if err != nil {
		return 0, err
	}
	entries := make([]blockTreeEntry, 0, len(leaves))
	for _, name := range radixNames(leaves) {
		entries = append(entries, leaves[name])
	}
	retained, err := transport.decodeBlockEntries(ctx, identity.LineageID, entries)
	if err != nil {
		return 0, err
	}
	blocks = append(blocks, retained...)
	// Validation of every old byte is complete before any archive mutation.
	for _, object := range scan.Objects {
		if _, err := s.Archive.Put(object, s.Endpoint()); err != nil {
			return 0, err
		}
	}
	index := recordsync.Index{DB: s.DB, ProjectID: s.ProjectID, Lineage: identity.LineageID, Now: s.Now}
	for _, block := range blocks {
		if _, err := index.ObservePublication(ctx, *s.Archive, s.Endpoint(), block); err != nil {
			return 0, err
		}
	}
	tree := commit.Tree
	for _, namespace := range namespaces {
		tree, err = transport.withoutNamespace(ctx, tree, namespace)
		if err != nil {
			return 0, err
		}
	}
	tree, count, err := transport.appendBlocks(ctx, tree, identity.LineageID, blocks, len(branch.OID))
	if err != nil {
		return 0, err
	}
	if _, err := transport.blockDeltaForConversion(ctx, commit.Tree, tree, identity.LineageID, true); err != nil {
		return 0, err
	}
	message := fmt.Sprintf("continuity: %s convert to format 5", identity.InstanceID)
	next, err := transport.writeCommit(ctx, tree, []string{branch.OID}, message)
	if err != nil {
		return 0, err
	}
	if err := transport.advanceBranch(ctx, branch, next); err != nil {
		return 0, err
	}
	return count, nil
}
