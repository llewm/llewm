package memsync

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"path"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// objectTransport operates entirely on verified Git objects. It does not need
// an index or working tree and supplies the normal memory-clone route.
// Its immutable tree merge never invokes text merge, hooks or repository code.
type objectTransport struct {
	repo         git
	trees        blockTreeDiscovery
	writtenTrees map[string]bool
}

type objectLeafChange struct {
	path          string
	before, after blockTreeEntry
}

// changedLeaves selects one namespace and descends only where tree identities
// differ. Empty before denotes an explicit initial/recovery inventory. The
// caller must discard the result on error (including the bounded-delta limit).
func (t *objectTransport) changedLeaves(ctx context.Context, before, after, directory string, limit int) ([]objectLeafChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validGitOID(after) || (before != "" && (!validGitOID(before) || len(before) != len(after))) || directory == "." || path.Clean(directory) != directory || strings.HasPrefix(directory, "/") || strings.ContainsAny(directory, "\x00\\") || limit < 1 {
		return nil, fmt.Errorf("invalid transport delta selection")
	}
	parts := strings.Split(directory, "/")
	if len(parts) > 32 {
		return nil, fmt.Errorf("transport namespace nesting exceeds bounds")
	}
	for _, name := range parts {
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("invalid transport namespace")
		}
	}
	if before == after {
		return nil, nil
	}
	lookup := func(tree string) (string, error) {
		for _, name := range parts {
			if tree == "" {
				return "", nil
			}
			entries, err := t.trees.read(ctx, t.repo, tree)
			if err != nil {
				return "", err
			}
			tree = ""
			for _, entry := range entries {
				if entry.name == name {
					if tree != "" || entry.kind != "tree" || entry.mode != "40000" {
						return "", fmt.Errorf("invalid transport namespace tree")
					}
					tree = entry.oid
				}
			}
		}
		return tree, nil
	}
	oldTree, err := lookup(before)
	if err != nil {
		return nil, err
	}
	newTree, err := lookup(after)
	if err != nil {
		return nil, err
	}
	var changes []objectLeafChange
	var walk func(string, string, string, int) error
	walk = func(old, next, prefix string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if old == next {
			return nil
		}
		if depth > 32 {
			return fmt.Errorf("transport delta nesting exceeds bounds")
		}
		sets := [2]map[string]blockTreeEntry{}
		names := map[string]bool{}
		for i, tree := range []string{old, next} {
			sets[i] = map[string]blockTreeEntry{}
			if tree == "" {
				continue
			}
			entries, err := t.trees.read(ctx, t.repo, tree)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if _, duplicate := sets[i][entry.name]; duplicate || (entry.kind != "tree" && (entry.kind != "blob" || entry.mode != "100644")) {
					return fmt.Errorf("invalid selected transport entry")
				}
				sets[i][entry.name] = entry
				names[entry.name] = true
			}
		}
		// Determinism makes bounded fallbacks and recovery reproducible.
		ordered := make([]string, 0, len(names))
		for name := range names {
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		for _, name := range ordered {
			a, b := sets[0][name], sets[1][name]
			if a == b {
				continue
			}
			child := path.Join(prefix, name)
			if a.kind == "tree" || b.kind == "tree" {
				if (a.name != "" && a.kind != "tree") || (b.name != "" && b.kind != "tree") {
					return fmt.Errorf("transport entry changed type")
				}
				if err := walk(a.oid, b.oid, child, depth+1); err != nil {
					return err
				}
			} else {
				if len(changes) == limit {
					return fmt.Errorf("transport delta exceeds bound")
				}
				changes = append(changes, objectLeafChange{path: child, before: a, after: b})
			}
		}
		return nil
	}
	if err := walk(oldTree, newTree, directory, 0); err != nil {
		return nil, err
	}
	syncwork.RecordWork(ctx, "transport_changed_leaves", int64(len(changes)))
	return changes, nil
}

func gitObjectIdentity(kind string, body []byte, width int) (string, error) {
	var h hash.Hash
	switch width {
	case 40:
		h = sha1.New() // Git SHA-1 object format, not a new security protocol.
	case 64:
		h = sha256.New()
	default:
		return "", fmt.Errorf("unsupported Git object identity")
	}
	fmt.Fprintf(h, "%s %d\x00", kind, len(body))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

type boundedObjectBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedObjectBuffer) Len() int       { return b.buffer.Len() }
func (b *boundedObjectBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedObjectBuffer) String() string { return b.buffer.String() }

func (b *boundedObjectBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("Git object exceeds bound")
	}
	return b.buffer.Write(p)
}

// readBlob requires a regular non-executable transport leaf. Opaque entries
// outside the selected transport namespace may be retained by mergeTrees but
// must never be decoded as transport records through symlinks or submodules.
func (t *objectTransport) readBlob(ctx context.Context, entry blockTreeEntry, limit int) ([]byte, error) {
	if entry.mode != "100644" || entry.kind != "blob" || !validGitOID(entry.oid) || limit < 0 {
		return nil, fmt.Errorf("invalid transport blob entry")
	}
	var body = boundedObjectBuffer{limit: limit}
	if err := t.repo.execOut(ctx, nil, &body, []string{"cat-file", "blob", entry.oid}); err != nil {
		return nil, err
	}
	actual, err := gitObjectIdentity("blob", body.Bytes(), len(entry.oid))
	if err != nil || actual != entry.oid {
		return nil, fmt.Errorf("Git blob content identity mismatch")
	}
	syncwork.RecordWork(ctx, "transport_blobs_read", 1)
	return body.Bytes(), nil
}

func validGitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// writeObject verifies Git's answer against independently computed identity;
// bytes travel on stdin and stdout without whitespace normalization.
func (t *objectTransport) writeObject(ctx context.Context, kind string, body []byte, width int) (string, error) {
	if kind != "blob" && kind != "tree" {
		return "", fmt.Errorf("unsupported transport object kind")
	}
	expected, err := gitObjectIdentity(kind, body, width)
	if err != nil {
		return "", err
	}
	var output = boundedObjectBuffer{limit: 65}
	repo := t.repo
	repo.Durable = true
	if err := repo.execOut(ctx, bytes.NewReader(body), &output, []string{"hash-object", "-w", "-t", kind, "--stdin"}); err != nil {
		return "", err
	}
	if output.String() != expected+"\n" {
		return "", fmt.Errorf("written Git object identity differs")
	}
	syncwork.RecordWork(ctx, "transport_objects_written", 1)
	return expected, nil
}

func (t *objectTransport) writeTree(ctx context.Context, entries []blockTreeEntry, width int) (string, error) {
	// Git sorts tree names as if their trailing character were '/'. This can
	// differ from plain name order (for example, directory 'a' and file 'a.c').
	entries = append([]blockTreeEntry(nil), entries...)
	key := func(e blockTreeEntry) string {
		if e.kind == "tree" {
			return e.name + "/"
		}
		return e.name
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
	var raw boundedTreeBuffer
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.name == "" || entry.name == "." || entry.name == ".." || strings.ContainsAny(entry.name, "/\x00") || seen[entry.name] || !validGitOID(entry.oid) || len(entry.oid) != width {
			return "", fmt.Errorf("invalid transport tree entry")
		}
		seen[entry.name] = true
		kind := "blob"
		switch entry.mode {
		case "40000":
			kind = "tree"
		case "160000":
			kind = "commit"
		case "100644", "100755", "120000":
		default:
			return "", fmt.Errorf("invalid transport tree mode")
		}
		if entry.kind != kind {
			return "", fmt.Errorf("transport tree mode differs from kind")
		}
		oid, _ := hex.DecodeString(entry.oid)
		if _, err := raw.Write(append(append([]byte(entry.mode+" "+entry.name), 0), oid...)); err != nil {
			return "", err
		}
	}
	oid, err := t.writeObject(ctx, "tree", raw.Bytes(), width)
	if err == nil {
		if t.writtenTrees == nil {
			t.writtenTrees = map[string]bool{}
		}
		t.writtenTrees[oid] = true
	}
	return oid, err
}

// mergeTrees performs a three-way, object-identity merge. Equal subtrees are
// reused without listing their descendants; only changed branches are opened.
// Concurrent unequal leaves, type changes and delete/modify conflicts fail
// closed. Protocol validation of new selected namespace objects remains the
// caller's responsibility before publishing the resulting commit.
func (t *objectTransport) mergeTrees(ctx context.Context, base, local, remote string) (string, error) {
	if !validGitOID(local) || !validGitOID(remote) || len(local) != len(remote) || (base != "" && (!validGitOID(base) || len(base) != len(local))) {
		return "", fmt.Errorf("invalid transport merge identity")
	}
	// Ownership is scoped to this merge, excluding trees built or read earlier.
	// Normalization may open only these outputs, not unchanged input history.
	t.writtenTrees = map[string]bool{}
	return t.mergeTree(ctx, base, local, remote, 0)
}

func (t *objectTransport) mergeTree(ctx context.Context, base, local, remote string, depth int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if local == remote || remote == base {
		return local, nil
	}
	if local == base {
		return remote, nil
	}
	if depth > 36 {
		return "", fmt.Errorf("transport merge nesting exceeds bounds")
	}
	sets := [3]map[string]blockTreeEntry{}
	names := map[string]bool{}
	for i, oid := range []string{base, local, remote} {
		sets[i] = map[string]blockTreeEntry{}
		if oid == "" {
			continue
		}
		entries, err := t.trees.read(ctx, t.repo, oid)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if _, duplicate := sets[i][entry.name]; duplicate {
				return "", fmt.Errorf("duplicate transport tree name")
			}
			sets[i][entry.name] = entry
			names[entry.name] = true
		}
	}
	var merged []blockTreeEntry
	for name := range names {
		b, l, r := sets[0][name], sets[1][name], sets[2][name]
		var entry blockTreeEntry
		switch {
		case l == r || r == b:
			entry = l
		case l == b:
			entry = r
		case l.kind == "tree" && r.kind == "tree" && (b.name == "" || b.kind == "tree"):
			oid, err := t.mergeTree(ctx, b.oid, l.oid, r.oid, depth+1)
			if err != nil {
				return "", err
			}
			entry = blockTreeEntry{name: name, kind: "tree", mode: "40000", oid: oid}
		default:
			return "", fmt.Errorf("concurrent immutable transport entry conflict")
		}
		if entry.name != "" {
			merged = append(merged, entry)
		}
	}
	return t.writeTree(ctx, merged, len(local))
}
