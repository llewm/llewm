package memsync

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
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

// validateBlockLayout refuses unreadable formats in this lineage while leaving
// other lineages and opaque unrelated root entries untouched. Old-format
// conversion must use its explicit reader before invoking ordinary publication.
func (t *objectTransport) validateBlockLayout(ctx context.Context, tree, lineage string) error {
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
		if entry.name != "v4" {
			return fmt.Errorf("history format %s for this lineage requires explicit conversion or a newer reader", entry.name)
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

// blockDelta verifies every new selected blob and its wire bytes. Unchanged
// subtree identities can be reused after prior verification; deleting or
// replacing a selected immutable path is never an ordinary arrival.
func (t *objectTransport) blockDelta(ctx context.Context, before, after, lineage string) ([]recordsync.Block, error) {
	if err := t.validateBlockLayout(ctx, before, lineage); err != nil {
		return nil, err
	}
	if err := t.validateBlockLayout(ctx, after, lineage); err != nil {
		return nil, err
	}
	directory, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return nil, err
	}
	directory = filepath.ToSlash(directory)
	changes, err := t.changedLeaves(ctx, before, after, directory, portable.MaxRecordCount)
	if err != nil {
		return nil, err
	}
	var blocks []recordsync.Block
	total, count := 0, 0
	for _, change := range changes {
		if change.before.name != "" || change.after.name == "" {
			return nil, fmt.Errorf("immutable history block was replaced or removed")
		}
		hash := strings.TrimSuffix(path.Base(change.path), ".block")
		if !validDigest(hash) || change.path != directory+"/"+hash+".block" {
			return nil, fmt.Errorf("noncanonical history block path")
		}
		body, err := t.readBlob(ctx, change.after, recordsync.MaxBlockBytes)
		if err != nil {
			return nil, err
		}
		block, err := recordsync.DecodeBlock(lineage, hash, body)
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

// appendBlocks builds a new tree from exact, validated blocks. It changes only
// the selected lineage and never writes an index or working tree. Existing
// canonical flat block paths and all unrelated tree entries retain their IDs.
// A flat block directory still needs listing when changed; this primitive alone
// does not establish bounded end-to-end discovery across growing histories.
func (t *objectTransport) appendBlocks(ctx context.Context, before, lineage string, blocks []recordsync.Block, width int) (string, int, error) {
	if (before != "" && (!validGitOID(before) || len(before) != width)) || (width != 40 && width != 64) {
		return "", 0, fmt.Errorf("invalid history publication tree")
	}
	if err := t.validateBlockLayout(ctx, before, lineage); err != nil {
		return "", 0, err
	}
	directory, err := recordsync.BlockDirectory(lineage)
	if err != nil {
		return "", 0, err
	}
	if len(blocks) > portable.MaxRecordCount {
		return "", 0, fmt.Errorf("history publication exceeds bounds")
	}
	additions := map[string]blockTreeEntry{}
	counts := map[string]int{}
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
		counts[name] = len(verified.Objects)
	}
	if len(additions) == 0 {
		return before, 0, nil
	}
	added := 0
	var edit func(string, []string) (string, error)
	edit = func(tree string, parts []string) (string, error) {
		var entries []blockTreeEntry
		if tree != "" {
			var err error
			entries, err = t.trees.read(ctx, t.repo, tree)
			if err != nil {
				return "", err
			}
		}
		entries = append([]blockTreeEntry(nil), entries...)
		if len(parts) == 0 {
			pending := make(map[string]blockTreeEntry, len(additions))
			for name, entry := range additions {
				pending[name] = entry
			}
			for _, entry := range entries {
				hash := strings.TrimSuffix(entry.name, ".block")
				if entry.mode != "100644" || entry.kind != "blob" || !validDigest(hash) || entry.name != hash+".block" {
					return "", fmt.Errorf("invalid existing history block entry")
				}
				if next, ok := pending[entry.name]; ok {
					if next != entry {
						return "", fmt.Errorf("immutable block publication conflicts with existing bytes")
					}
					delete(pending, entry.name)
				}
			}
			if len(pending) == 0 {
				return tree, nil
			}
			for name, entry := range pending {
				entries = append(entries, entry)
				added += counts[name]
			}
			return t.writeTree(ctx, entries, width)
		}
		index := -1
		child := ""
		for i, entry := range entries {
			if entry.name == parts[0] {
				if index != -1 || entry.kind != "tree" || entry.mode != "40000" {
					return "", fmt.Errorf("invalid history publication namespace")
				}
				index, child = i, entry.oid
			}
		}
		next, err := edit(child, parts[1:])
		if err != nil || next == child {
			return tree, err
		}
		entry := blockTreeEntry{name: parts[0], kind: "tree", mode: "40000", oid: next}
		if index == -1 {
			entries = append(entries, entry)
		} else {
			entries[index] = entry
		}
		return t.writeTree(ctx, entries, width)
	}
	after, err := edit(before, strings.Split(filepath.ToSlash(directory), "/"))
	if err != nil {
		return "", 0, err
	}
	return after, added, nil
}
