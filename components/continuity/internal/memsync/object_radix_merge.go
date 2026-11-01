package memsync

import (
	"context"
	"fmt"
	"strings"
)

// normalizeRadix repairs only object trees written by the current merge.
// Valid input trees may merge into an oversized leaf bucket or a mixed
// leaf/branch node. Unchanged verified input subtrees remain opaque.
func (t *objectTransport) normalizeRadix(ctx context.Context, tree, prefix string, width int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !t.writtenTrees[tree] {
		return tree, nil
	}
	if len(prefix) > 64 || len(prefix)%2 != 0 || strings.Trim(prefix, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid history radix normalization prefix")
	}
	entries, err := t.trees.read(ctx, t.repo, tree)
	if err != nil {
		return "", err
	}
	if len(entries) > 320 {
		return "", fmt.Errorf("merged history radix node exceeds bounds")
	}
	leaves := map[string]blockTreeEntry{}
	children := map[string]blockTreeEntry{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.name] {
			return "", fmt.Errorf("duplicate merged history radix entry")
		}
		seen[entry.name] = true
		if entry.kind == "tree" && entry.mode == "40000" {
			if len(prefix) == 64 || len(entry.name) != 2 || strings.Trim(entry.name, "0123456789abcdef") != "" {
				return "", fmt.Errorf("invalid merged history radix branch")
			}
			children[entry.name] = entry
		} else {
			hash := strings.TrimSuffix(entry.name, ".block")
			if entry.kind != "blob" || entry.mode != "100644" || !validDigest(hash) || entry.name != hash+".block" || !strings.HasPrefix(hash, prefix) {
				return "", fmt.Errorf("invalid merged history radix leaf")
			}
			leaves[entry.name] = entry
		}
	}
	if len(leaves) > 128 {
		return "", fmt.Errorf("merged history radix leaf bound exceeded")
	}
	if len(children) == 0 {
		return t.writeRadixLeaves(ctx, prefix, leaves, width)
	}
	changed := false
	for _, part := range radixNames(children) {
		entry := children[part]
		next, err := t.normalizeRadix(ctx, entry.oid, prefix+part, width)
		if err != nil {
			return "", err
		}
		if next != entry.oid {
			entry.oid = next
			children[part] = entry
			changed = true
		}
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
		prior := children[part]
		next, _, err := t.appendRadix(ctx, prior.oid, prefix+part, group, width)
		if err != nil {
			return "", err
		}
		children[part] = blockTreeEntry{name: part, kind: "tree", mode: "40000", oid: next}
		changed = true
	}
	if !changed {
		return tree, nil
	}
	normalized := make([]blockTreeEntry, 0, len(children))
	for _, entry := range children {
		normalized = append(normalized, entry)
	}
	return t.writeTree(ctx, normalized, width)
}
