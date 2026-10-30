package recordsync

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// ManifestDelta reports catalog membership changes, not record validation or
// file integrity. Equal subtree hashes reuse the caller's previously validated
// inventory. A caller without that inventory must use ManifestBlocks instead.
type ManifestDelta struct {
	Added, Removed []string
	VisitedNodes   int
}

func readManifestAnnouncement(root *os.Root, lineage, hash string) error {
	path, err := ManifestRootPath(lineage, hash)
	if err != nil {
		return err
	}
	body, err := ReadFile(root, path, MaxManifestBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(body, []byte(manifestHeader+lineage+"\n"+hash+"\n")) {
		return fmt.Errorf("invalid manifest root announcement")
	}
	return nil
}

// ChangedManifestBlocks visits changed radix paths only. Both roots must be
// durably announced. It never treats absent/damaged metadata as an empty tree;
// callers retain the full scan fallback when discovery is uncertain.
func ChangedManifestBlocks(ctx context.Context, root *os.Root, lineage, before, after string) (ManifestDelta, error) {
	var result ManifestDelta
	for _, hash := range []string{before, after} {
		if err := readManifestAnnouncement(root, lineage, hash); err != nil {
			return ManifestDelta{}, err
		}
	}
	read := func(hash, prefix string) (Manifest, error) {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		result.VisitedNodes++
		if result.VisitedNodes > MaxScanEntries {
			return Manifest{}, fmt.Errorf("manifest delta traversal exceeds bounds")
		}
		path, err := manifestPath(lineage, hash)
		if err != nil {
			return Manifest{}, err
		}
		body, err := ReadFile(root, path, MaxManifestBytes)
		if err != nil {
			return Manifest{}, err
		}
		return DecodeManifest(lineage, hash, prefix, body)
	}
	var collect func(Manifest, *[]string) error
	collect = func(m Manifest, values *[]string) error {
		*values = append(*values, m.Blocks...)
		if len(*values) > portable.MaxRecordCount {
			return fmt.Errorf("manifest delta membership exceeds bounds")
		}
		for _, digit := range "0123456789abcdef" {
			key := m.Prefix + string(digit)
			if hash, ok := m.Children[key]; ok {
				child, err := read(hash, key)
				if err != nil {
					return err
				}
				if err := collect(child, values); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var compare func(string, string, string) error
	compare = func(old, current, prefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if old == current {
			return nil
		}
		var left, right Manifest
		var err error
		if old != "" {
			left, err = read(old, prefix)
			if err != nil {
				return err
			}
		}
		if current != "" {
			right, err = read(current, prefix)
			if err != nil {
				return err
			}
		}
		if left.Children != nil && right.Children != nil {
			for _, digit := range "0123456789abcdef" {
				key := prefix + string(digit)
				if err := compare(left.Children[key], right.Children[key], key); err != nil {
					return err
				}
			}
			return nil
		}
		var removed, added []string
		if err := collect(left, &removed); err != nil {
			return err
		}
		if err := collect(right, &added); err != nil {
			return err
		}
		prior := make(map[string]bool, len(removed))
		for _, value := range removed {
			prior[value] = true
		}
		for _, value := range added {
			if !prior[value] {
				result.Added = append(result.Added, value)
			}
			delete(prior, value)
		}
		for value := range prior {
			result.Removed = append(result.Removed, value)
		}
		if len(result.Added)+len(result.Removed) > 2*portable.MaxRecordCount {
			return fmt.Errorf("manifest delta exceeds bounds")
		}
		return nil
	}
	if err := compare(before, after, ""); err != nil {
		return ManifestDelta{}, err
	}
	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	return result, nil
}
