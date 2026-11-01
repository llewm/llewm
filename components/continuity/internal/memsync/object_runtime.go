package memsync

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func (s Syncer) objectReader(repo git) *objectTransport {
	if s.objects != nil {
		return s.objects
	}
	return &objectTransport{repo: repo}
}

func (t *objectTransport) headTree(ctx context.Context) (string, error) {
	branch, err := t.currentBranch(ctx)
	if err != nil || branch.OID == "" {
		return "", err
	}
	commit, err := t.readCommit(ctx, branch.OID)
	return commit.Tree, err
}

// legacySnapshot verifies every selected legacy blob directly from its Git
// identity. Conversion uses this bounded complete reader before removing any
// old namespace; it never depends on an index or checked-out file.
func (t *objectTransport) legacySnapshot(ctx context.Context, tree, lineage string, previous bool) (recordsync.ScanResult, error) {
	var result recordsync.ScanResult
	if _, err := recordsync.BlockDirectory(lineage); err != nil {
		return result, err
	}
	if tree == "" {
		return result, nil
	}
	directory := portable.MemoryLineageDir(lineage)
	version := portable.MemoryRepositoryRoot
	if previous {
		directory = portable.FormerMemoryLineageDir(lineage)
		version = portable.FormerMemoryRepositoryRoot
	}
	selected, err := t.treeAt(ctx, tree, version, strings.TrimPrefix(lineage, "lineage:"))
	if err != nil {
		return result, err
	}
	if selected != "" {
		entries, err := t.trees.read(ctx, t.repo, selected)
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if entry.name != "records" || entry.kind != "tree" || entry.mode != "40000" {
				return result, fmt.Errorf("unrecognized legacy lineage namespace")
			}
		}
	}
	leaves, err := t.changedLeaves(ctx, "", tree, filepath.ToSlash(directory), portable.MaxRecordCount)
	if err != nil {
		return result, err
	}
	total := 0
	for _, change := range leaves {
		body, err := t.readBlob(ctx, change.after, recordsync.MaxObjectBytes)
		if err != nil {
			return result, err
		}
		var object portable.EncodedEnvelope
		if previous {
			object, err = portable.ConvertPreviousMemoryFile(change.path, body)
		} else {
			object, err = portable.DecodeTransportEnvelope(body)
			if err == nil && (object.Envelope.LineageID != lineage || filepath.ToSlash(portable.MemoryRecordPath(object.Envelope)) != change.path) {
				err = fmt.Errorf("legacy observation has wrong lineage or path")
			}
		}
		if err != nil {
			return result, fmt.Errorf("invalid legacy observation at %s: %w", change.path, err)
		}
		if object.Envelope.LineageID != lineage {
			return result, fmt.Errorf("legacy observation has wrong lineage")
		}
		total += len(body)
		if total > portable.MaxRecordsBytes {
			return result, fmt.Errorf("legacy history inventory exceeds bounds")
		}
		result.Objects = append(result.Objects, object)
	}
	return result, nil
}

// withoutNamespace is reserved for explicit conversion after the entire
// selected legacy namespace has been validated and archived. Empty ancestor
// directories are pruned, leaving all other lineages and old commits intact.
func (t *objectTransport) withoutNamespace(ctx context.Context, tree, directory string) (string, error) {
	if !validGitOID(tree) || filepath.ToSlash(filepath.Clean(directory)) != directory || strings.ContainsAny(directory, "\\\x00") {
		return "", fmt.Errorf("invalid legacy namespace removal")
	}
	parts := strings.Split(directory, "/")
	if len(parts) < 2 || len(parts) > 32 {
		return "", fmt.Errorf("invalid legacy namespace removal depth")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid legacy namespace removal path")
		}
	}
	var remove func(string, []string) (string, error)
	remove = func(oid string, parts []string) (string, error) {
		entries, err := t.trees.read(ctx, t.repo, oid)
		if err != nil {
			return "", err
		}
		for i, entry := range entries {
			if entry.name != parts[0] {
				continue
			}
			if entry.kind != "tree" || entry.mode != "40000" {
				return "", fmt.Errorf("legacy namespace is not a regular tree")
			}
			next := ""
			if len(parts) != 1 {
				next, err = remove(entry.oid, parts[1:])
				if err != nil || next == entry.oid {
					return oid, err
				}
			}
			updated := append([]blockTreeEntry(nil), entries...)
			if next == "" {
				updated = append(updated[:i], updated[i+1:]...)
			} else {
				updated[i].oid = next
			}
			if len(updated) == 0 {
				return "", nil
			}
			return t.writeTree(ctx, updated, len(tree))
		}
		return oid, nil
	}
	after, err := remove(tree, parts)
	if err != nil {
		return "", err
	}
	if after == "" {
		return t.writeTree(ctx, nil, len(tree))
	}
	return after, nil
}
