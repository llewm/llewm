package memsync

import (
	"context"
	"fmt"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func (t *objectTransport) treeAt(ctx context.Context, tree string, parts ...string) (string, error) {
	for _, name := range parts {
		if tree == "" {
			return "", nil
		}
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
			return "", fmt.Errorf("invalid transport tree selection")
		}
		entries, err := t.trees.read(ctx, t.repo, tree)
		if err != nil {
			return "", err
		}
		tree = ""
		for _, entry := range entries {
			if entry.name != name {
				continue
			}
			if tree != "" || entry.kind != "tree" || entry.mode != "40000" {
				return "", fmt.Errorf("invalid selected transport namespace")
			}
			tree = entry.oid
		}
	}
	return tree, nil
}

func gitBlockDirectory(lineage string) (string, error) {
	if _, err := recordsync.BlockDirectory(lineage); err != nil {
		return "", err
	}
	return "v5/" + strings.TrimPrefix(lineage, "lineage:") + "/blocks", nil
}

func (t *objectTransport) blockDelta(ctx context.Context, before, after, lineage string) ([]recordsync.Block, error) {
	return t.blockDeltaForConversion(ctx, before, after, lineage, false)
}

func (t *objectTransport) blockDeltaForConversion(ctx context.Context, before, after, lineage string, converting bool) ([]recordsync.Block, error) {
	for _, tree := range []string{before, after} {
		if err := t.validateBlockLayoutForConversion(ctx, tree, lineage, converting); err != nil {
			return nil, err
		}
	}
	var entries []blockTreeEntry
	if converting {
		old, err := t.conversionBlockLeaves(ctx, before, lineage)
		if err != nil {
			return nil, err
		}
		next, err := t.conversionBlockLeaves(ctx, after, lineage)
		if err != nil {
			return nil, err
		}
		for name, leaf := range old {
			if next[name] != leaf {
				return nil, fmt.Errorf("conversion removed or replaced an immutable block")
			}
		}
		for _, name := range radixNames(next) {
			if old[name].name == "" {
				entries = append(entries, next[name])
			}
		}
	} else {
		directory, _ := gitBlockDirectory(lineage)
		old, err := t.treeAt(ctx, before, strings.Split(directory, "/")...)
		if err != nil {
			return nil, err
		}
		next, err := t.treeAt(ctx, after, strings.Split(directory, "/")...)
		if err != nil {
			return nil, err
		}
		if next == "" {
			if old != "" {
				return nil, fmt.Errorf("immutable history blocks removed")
			}
			return nil, nil
		}
		entries, err = t.radixDelta(ctx, old, next, portable.MaxRecordCount)
		if err != nil {
			return nil, err
		}
	}
	return t.decodeBlockEntries(ctx, lineage, entries)
}

// Conversion alone inventories both layouts. The union is keyed by logical
// block hash and requires the same exact blob identity across relocation.
func (t *objectTransport) conversionBlockLeaves(ctx context.Context, tree, lineage string) (map[string]blockTreeEntry, error) {
	leaves := map[string]blockTreeEntry{}
	for _, version := range []string{"v4", "v5"} {
		selected, err := t.treeAt(ctx, tree, version, strings.TrimPrefix(lineage, "lineage:"), "blocks")
		if err != nil {
			return nil, err
		}
		if selected == "" {
			continue
		}
		var entries []blockTreeEntry
		if version == "v5" {
			entries, err = t.radixDelta(ctx, "", selected, portable.MaxRecordCount)
		} else {
			entries, err = t.trees.read(ctx, t.repo, selected)
		}
		if err != nil {
			return nil, err
		}
		for _, leaf := range entries {
			hash := strings.TrimSuffix(leaf.name, ".block")
			if leaf.kind != "blob" || leaf.mode != "100644" || !validDigest(hash) || leaf.name != hash+".block" {
				return nil, fmt.Errorf("invalid conversion block path")
			}
			if old, ok := leaves[leaf.name]; ok && old != leaf {
				return nil, fmt.Errorf("conversion block identity collision")
			}
			leaves[leaf.name] = leaf
			if len(leaves) > portable.MaxRecordCount {
				return nil, fmt.Errorf("conversion inventory exceeds bounds")
			}
		}
	}
	return leaves, nil
}

func (t *objectTransport) decodeBlockEntries(ctx context.Context, lineage string, entries []blockTreeEntry) ([]recordsync.Block, error) {
	var blocks []recordsync.Block
	total, count := 0, 0
	for _, entry := range entries {
		body, err := t.readBlob(ctx, entry, recordsync.MaxBlockBytes)
		if err != nil {
			return nil, err
		}
		block, err := recordsync.DecodeBlock(lineage, strings.TrimSuffix(entry.name, ".block"), body)
		if err != nil {
			return nil, err
		}
		total += len(body)
		count += len(block.Objects)
		if total > portable.MaxRecordsBytes || count > portable.MaxRecordCount {
			return nil, fmt.Errorf("history block delta exceeds bounds")
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (t *objectTransport) appendBlocks(ctx context.Context, before, lineage string, blocks []recordsync.Block, width int) (string, int, error) {
	return t.appendBlocksForConversion(ctx, before, lineage, blocks, width, false)
}

func (t *objectTransport) appendBlocksForConversion(ctx context.Context, before, lineage string, blocks []recordsync.Block, width int, converting bool) (string, int, error) {
	if (before != "" && (!validGitOID(before) || len(before) != width)) || (width != 40 && width != 64) || len(blocks) > portable.MaxRecordCount {
		return "", 0, fmt.Errorf("invalid history publication")
	}
	if err := t.validateBlockLayoutForConversion(ctx, before, lineage, converting); err != nil {
		return "", 0, err
	}
	additions := map[string]blockTreeEntry{}
	counts, byteCounts := map[string]int{}, map[string]int{}
	total, count := 0, 0
	for _, block := range blocks {
		verified, err := recordsync.DecodeBlock(lineage, block.Hash, block.Bytes)
		if err != nil {
			return "", 0, err
		}
		total += len(verified.Bytes)
		count += len(verified.Objects)
		if total > portable.MaxRecordsBytes || count > portable.MaxRecordCount {
			return "", 0, fmt.Errorf("history publication exceeds bounds")
		}
		oid, err := t.writeObject(ctx, "blob", verified.Bytes, width)
		if err != nil {
			return "", 0, err
		}
		name := verified.Hash + ".block"
		additions[name] = blockTreeEntry{name: name, kind: "blob", mode: "100644", oid: oid}
		counts[name], byteCounts[name] = len(verified.Objects), len(verified.Bytes)
	}
	if len(additions) == 0 {
		return before, 0, nil
	}
	directory, _ := gitBlockDirectory(lineage)
	added := 0
	after, err := t.editNamespace(ctx, before, strings.Split(directory, "/"), width, func(tree string) (string, error) {
		next, newLeaves, err := t.appendRadix(ctx, tree, "", additions, width)
		if err != nil {
			return "", err
		}
		for name := range newLeaves {
			added += counts[name]
			syncwork.RecordWork(ctx, "published_block_bytes", int64(byteCounts[name]))
		}
		return next, nil
	})
	if err != nil {
		return "", 0, err
	}
	return after, added, nil
}

// editNamespace preserves every unrelated entry and rewrites only ancestors of
// the selected tree, without an index or working tree.
func (t *objectTransport) editNamespace(ctx context.Context, tree string, parts []string, width int, edit func(string) (string, error)) (string, error) {
	if len(parts) == 0 {
		return edit(tree)
	}
	if parts[0] == "" || strings.ContainsAny(parts[0], "/\x00") || parts[0] == "." || parts[0] == ".." {
		return "", fmt.Errorf("invalid history namespace")
	}
	var entries []blockTreeEntry
	if tree != "" {
		var err error
		entries, err = t.trees.read(ctx, t.repo, tree)
		if err != nil {
			return "", err
		}
	}
	entries = append([]blockTreeEntry(nil), entries...)
	index, child := -1, ""
	for i, entry := range entries {
		if entry.name == parts[0] {
			if index != -1 || entry.kind != "tree" || entry.mode != "40000" {
				return "", fmt.Errorf("invalid history namespace tree")
			}
			index, child = i, entry.oid
		}
	}
	next, err := t.editNamespace(ctx, child, parts[1:], width, edit)
	if err != nil || next == child {
		return tree, err
	}
	entry := blockTreeEntry{name: parts[0], kind: "tree", mode: "40000", oid: next}
	if index < 0 {
		entries = append(entries, entry)
	} else {
		entries[index] = entry
	}
	return t.writeTree(ctx, entries, width)
}

func (t *objectTransport) finishBlockMerge(lineage string, converting bool) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, local, tree string) (string, error) {
		directory, err := gitBlockDirectory(lineage)
		if err != nil {
			return "", err
		}
		selected, err := t.treeAt(ctx, tree, strings.Split(directory, "/")...)
		if err != nil {
			return "", err
		}
		if selected != "" {
			tree, err = t.editNamespace(ctx, tree, strings.Split(directory, "/"), len(tree), func(old string) (string, error) { return t.normalizeRadix(ctx, old, "", len(tree)) })
			if err != nil {
				return "", err
			}
		}
		if _, err := t.blockDeltaForConversion(ctx, local, tree, lineage, converting); err != nil {
			return "", err
		}
		return tree, nil
	}
}

// validateBlockLayout refuses unreadable formats in this lineage while leaving
// other lineages and opaque unrelated root entries untouched. Old-format
// conversion must use its explicit reader before invoking ordinary publication.
func (t *objectTransport) validateBlockLayout(ctx context.Context, tree, lineage string) error {
	return t.validateBlockLayoutForConversion(ctx, tree, lineage, false)
}

func (t *objectTransport) validateBlockLayoutForConversion(ctx context.Context, tree, lineage string, converting bool) error {
	if _, err := recordsync.BlockDirectory(lineage); err != nil {
		return err
	}
	if tree == "" {
		return nil
	}
	entries, err := t.trees.read(ctx, t.repo, tree)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(entry.name) < 2 || entry.name[0] != 'v' || entry.name[1] < '0' || entry.name[1] > '9' {
			continue
		}
		// A malformed version namespace cannot safely establish absence of the
		// selected lineage, including if represented by a symlink or submodule.
		if entry.kind != "tree" || entry.mode != "40000" {
			return fmt.Errorf("invalid versioned history namespace")
		}
		selected, err := t.treeAt(ctx, entry.oid, strings.TrimPrefix(lineage, "lineage:"))
		if err != nil {
			return err
		}
		if selected == "" {
			continue
		}
		if entry.name != "v5" {
			if entry.name == "v2" || entry.name == "v3" || entry.name == "v4" {
				if !converting {
					return fmt.Errorf("format-%s observations require explicit conversion: stop old writers, then run `llewm sync convert --writers-stopped`", entry.name[1:])
				}
				if entry.name != "v4" {
					continue
				}
			} else {
				return fmt.Errorf("history format %s for this lineage requires explicit conversion or a newer reader", entry.name)
			}
		}
		children, err := t.trees.read(ctx, t.repo, selected)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.name != "blocks" || child.kind != "tree" || child.mode != "40000" {
				return fmt.Errorf("unrecognized selected history namespace")
			}
		}
	}
	return nil
}
