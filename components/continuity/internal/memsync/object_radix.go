package memsync

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// A leaf bucket has at most 64 immutable blocks. Larger buckets branch on
// successive hash bytes, so each listed node has at most 256 children. These
// object primitives do not select or enable a transport wire format.
const radixBucketLimit = 64

type radixNode struct {
	branch  bool
	entries map[string]blockTreeEntry
}

func (t *objectTransport) readRadixNode(ctx context.Context, tree, prefix string) (radixNode, error) {
	node := radixNode{entries: map[string]blockTreeEntry{}}
	if len(prefix) > 64 || len(prefix)%2 != 0 || strings.Trim(prefix, "0123456789abcdef") != "" {
		return node, fmt.Errorf("invalid history radix prefix")
	}
	if tree == "" {
		return node, nil
	}
	entries, err := t.trees.read(ctx, t.repo, tree)
	if err != nil {
		return node, err
	}
	if len(entries) > 256 {
		return node, fmt.Errorf("history radix node exceeds bounds")
	}
	for i, entry := range entries {
		branch := entry.kind == "tree" && entry.mode == "40000"
		if i == 0 {
			node.branch = branch
		}
		if branch != node.branch || node.entries[entry.name].name != "" {
			return node, fmt.Errorf("mixed or duplicate history radix entries")
		}
		if branch {
			if len(prefix) == 64 || len(entry.name) != 2 || strings.Trim(entry.name, "0123456789abcdef") != "" {
				return node, fmt.Errorf("invalid history radix branch")
			}
		} else {
			hash := strings.TrimSuffix(entry.name, ".block")
			if entry.kind != "blob" || entry.mode != "100644" || !validDigest(hash) || entry.name != hash+".block" || !strings.HasPrefix(hash, prefix) || len(entries) > radixBucketLimit {
				return node, fmt.Errorf("invalid history radix leaf")
			}
		}
		node.entries[entry.name] = entry
	}
	return node, nil
}

func radixNames(entries map[string]blockTreeEntry) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t *objectTransport) writeRadixLeaves(ctx context.Context, prefix string, leaves map[string]blockTreeEntry, width int) (string, error) {
	entries := make([]blockTreeEntry, 0, len(leaves))
	if len(leaves) <= radixBucketLimit {
		for _, leaf := range leaves {
			entries = append(entries, leaf)
		}
	} else {
		if len(prefix) >= 64 {
			return "", fmt.Errorf("history radix hash exhausted")
		}
		groups := map[string]map[string]blockTreeEntry{}
		for name, leaf := range leaves {
			part := name[len(prefix) : len(prefix)+2]
			if groups[part] == nil {
				groups[part] = map[string]blockTreeEntry{}
			}
			groups[part][name] = leaf
		}
		for part, group := range groups {
			child, err := t.writeRadixLeaves(ctx, prefix+part, group, width)
			if err != nil {
				return "", err
			}
			entries = append(entries, blockTreeEntry{name: part, kind: "tree", mode: "40000", oid: child})
		}
	}
	return t.writeTree(ctx, entries, width)
}

// appendRadix validates additions, reuses untouched branches and returns only
// genuinely new identities. Existing blobs need no body reads. The caller must
// have verified the previous tree and must verify new block bodies separately.
func (t *objectTransport) appendRadix(ctx context.Context, tree string, additions map[string]blockTreeEntry, width int) (string, map[string]blockTreeEntry, error) {
	if len(additions) > portable.MaxRecordCount || (width != 40 && width != 64) || (tree != "" && (!validGitOID(tree) || len(tree) != width)) {
		return "", nil, fmt.Errorf("invalid history radix publication")
	}
	for name, entry := range additions {
		hash := strings.TrimSuffix(name, ".block")
		if name != entry.name || !validDigest(hash) || name != hash+".block" || entry.kind != "blob" || entry.mode != "100644" || !validGitOID(entry.oid) || len(entry.oid) != width {
			return "", nil, fmt.Errorf("invalid history radix addition")
		}
	}
	added := map[string]blockTreeEntry{}
	var edit func(string, string, map[string]blockTreeEntry) (string, error)
	edit = func(old, prefix string, pending map[string]blockTreeEntry) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(pending) == 0 {
			return old, nil
		}
		node, err := t.readRadixNode(ctx, old, prefix)
		if err != nil {
			return "", err
		}
		if !node.branch {
			changed := false
			for name, entry := range pending {
				if previous, ok := node.entries[name]; ok {
					if previous != entry {
						return "", fmt.Errorf("immutable history radix block changed")
					}
				} else {
					node.entries[name], added[name], changed = entry, entry, true
				}
			}
			if !changed {
				return old, nil
			}
			return t.writeRadixLeaves(ctx, prefix, node.entries, width)
		}
		groups := map[string]map[string]blockTreeEntry{}
		for name, entry := range pending {
			part := name[len(prefix) : len(prefix)+2]
			if groups[part] == nil {
				groups[part] = map[string]blockTreeEntry{}
			}
			groups[part][name] = entry
		}
		changed := false
		for part, group := range groups {
			previous := node.entries[part]
			child, err := edit(previous.oid, prefix+part, group)
			if err != nil {
				return "", err
			}
			if child != previous.oid {
				node.entries[part] = blockTreeEntry{name: part, kind: "tree", mode: "40000", oid: child}
				changed = true
			}
		}
		if !changed {
			return old, nil
		}
		entries := make([]blockTreeEntry, 0, len(node.entries))
		for _, entry := range node.entries {
			entries = append(entries, entry)
		}
		return t.writeTree(ctx, entries, width)
	}
	after, err := edit(tree, "", additions)
	if err != nil {
		return "", nil, err
	}
	return after, added, nil
}

// radixDelta treats a leaf-to-branch split as structural relocation: every old
// hash must still reference the same blob. Only new identities are returned.
// Full/new subtrees are checked for canonical bucket sizes; verified unchanged
// subtrees are reused by identity, without walking their descendants.
func (t *objectTransport) radixDelta(ctx context.Context, before, after string, limit int) ([]blockTreeEntry, error) {
	if !validGitOID(after) || (before != "" && (!validGitOID(before) || len(before) != len(after))) || limit < 1 {
		return nil, fmt.Errorf("invalid history radix delta")
	}
	var arrivals []blockTreeEntry
	var inventory func(string, string, map[string]blockTreeEntry) (int, error)
	inventory = func(tree, prefix string, leaves map[string]blockTreeEntry) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		node, err := t.readRadixNode(ctx, tree, prefix)
		if err != nil {
			return 0, err
		}
		count := 0
		for _, name := range radixNames(node.entries) {
			entry := node.entries[name]
			if node.branch {
				n, err := inventory(entry.oid, prefix+name, leaves)
				if err != nil {
					return 0, err
				}
				if n == 0 {
					return 0, fmt.Errorf("empty history radix branch")
				}
				count += n
			} else {
				if len(leaves) >= portable.MaxRecordCount {
					return 0, fmt.Errorf("history radix inventory exceeds bounds")
				}
				leaves[name] = entry
				count++
			}
		}
		if node.branch && count <= radixBucketLimit {
			return 0, fmt.Errorf("noncanonical small history radix branch")
		}
		return count, nil
	}
	var walk func(string, string, string) error
	walk = func(old, next, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if old == next {
			return nil
		}
		a, err := t.readRadixNode(ctx, old, prefix)
		if err != nil {
			return err
		}
		b, err := t.readRadixNode(ctx, next, prefix)
		if err != nil {
			return err
		}
		if a.branch && !b.branch {
			return fmt.Errorf("immutable history radix branch removed")
		}
		if !a.branch {
			leaves := b.entries
			if b.branch {
				leaves = map[string]blockTreeEntry{}
				if _, err := inventory(next, prefix, leaves); err != nil {
					return err
				}
			}
			for name, entry := range a.entries {
				if leaves[name] != entry {
					return fmt.Errorf("immutable history radix block replaced or removed")
				}
			}
			for _, name := range radixNames(leaves) {
				if a.entries[name].name == "" {
					if len(arrivals) >= limit {
						return fmt.Errorf("history radix delta exceeds bounds")
					}
					arrivals = append(arrivals, leaves[name])
				}
			}
			return nil
		}
		for name := range a.entries {
			if b.entries[name].name == "" {
				return fmt.Errorf("immutable history radix branch removed")
			}
		}
		for _, name := range radixNames(b.entries) {
			if err := walk(a.entries[name].oid, b.entries[name].oid, prefix+name); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(before, after, ""); err != nil {
		return nil, err
	}
	return arrivals, nil
}
