package memsync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

const maxCachedTreeBytes = 4 << 20

// Cache entries contain raw Git tree bytes, not self-asserted membership lists.
// Their selected object ID independently verifies their complete content. A
// damaged, missing or unwritable cache only costs a Git read, never records.
func openBlockTreeCache(archive string) (*os.Root, error) {
	root, err := recordsync.OpenRoot(archive)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Status is disposable local bookkeeping, excluded from archive/provenance
	// receipts and transport publication. Root-relative operations confine I/O.
	if err := root.MkdirAll("status/git-trees", 0700); err != nil {
		return nil, err
	}
	return root.OpenRoot("status/git-trees")
}

type boundedTreeBuffer struct{ bytes.Buffer }

func (b *boundedTreeBuffer) Write(p []byte) (int, error) {
	if len(p) > maxCachedTreeBytes-b.Len() {
		return 0, fmt.Errorf("Git tree exceeds cache bound")
	}
	return b.Buffer.Write(p)
}

func (d *blockTreeDiscovery) treeEntries(ctx context.Context, repo git, oid string) ([]blockTreeEntry, error) {
	if d.cache != nil {
		if raw, err := recordsync.ReadFile(d.cache, oid+".tree", maxCachedTreeBytes); err == nil {
			if entries, err := decodeGitTree(oid, raw); err == nil {
				d.cacheHits++
				return entries, nil
			}
		}
	}
	var raw boundedTreeBuffer
	d.gitReads++
	if err := repo.execOut(ctx, nil, &raw, []string{"cat-file", "tree", oid}); err != nil {
		return nil, err
	}
	entries, err := decodeGitTree(oid, raw.Bytes())
	if err != nil {
		return nil, err
	}
	if d.cache != nil {
		_, _ = recordsync.WriteFile(d.cache, oid+".tree", raw.Bytes(), true)
	}
	return entries, nil
}

func decodeGitTree(oid string, raw []byte) ([]blockTreeEntry, error) {
	var h hash.Hash
	size := 0
	switch len(oid) {
	case 40:
		h = sha1.New()
		size = 20 // Git's SHA-1 object identity, not a new security protocol.
	case 64:
		h = sha256.New()
		size = 32
	default:
		return nil, fmt.Errorf("invalid Git tree identity")
	}
	if len(raw) > maxCachedTreeBytes {
		return nil, fmt.Errorf("Git tree exceeds cache bound")
	}
	fmt.Fprintf(h, "tree %d\x00", len(raw))
	h.Write(raw)
	if hex.EncodeToString(h.Sum(nil)) != oid {
		return nil, fmt.Errorf("Git tree content identity mismatch")
	}
	var entries []blockTreeEntry
	for len(raw) > 0 {
		space := bytes.IndexByte(raw, ' ')
		if space < 0 {
			return nil, fmt.Errorf("invalid Git tree mode")
		}
		mode := string(raw[:space])
		raw = raw[space+1:]
		end := bytes.IndexByte(raw, 0)
		if end < 0 || len(raw) < end+1+size {
			return nil, fmt.Errorf("truncated Git tree entry")
		}
		name := string(raw[:end])
		raw = raw[end+1:]
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return nil, fmt.Errorf("invalid Git tree name")
		}
		kind := "blob"
		switch mode {
		case "40000":
			kind = "tree"
		case "160000":
			kind = "commit"
		case "100644", "100755", "120000":
		default:
			return nil, fmt.Errorf("unsupported Git tree mode")
		}
		entries = append(entries, blockTreeEntry{name: name, kind: kind, oid: hex.EncodeToString(raw[:size])})
		raw = raw[size:]
	}
	return entries, nil
}
