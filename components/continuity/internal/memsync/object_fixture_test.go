package memsync

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func fixturePathTree(t testing.TB, transport *objectTransport, files map[string]blockTreeEntry, width int) string {
	t.Helper()
	type node struct {
		children map[string]*node
		leaf     *blockTreeEntry
	}
	root := &node{children: map[string]*node{}}
	for name, entry := range files {
		if path.Clean(name) != name || strings.HasPrefix(name, "/") {
			t.Fatalf("bad fixture path %q", name)
		}
		current := root
		parts := strings.Split(name, "/")
		for _, part := range parts {
			if current.children[part] == nil {
				current.children[part] = &node{children: map[string]*node{}}
			}
			current = current.children[part]
		}
		entry.name = parts[len(parts)-1]
		current.leaf = &entry
	}
	var build func(*node) string
	build = func(current *node) string {
		var entries []blockTreeEntry
		for name, child := range current.children {
			if child.leaf != nil {
				if len(child.children) != 0 {
					t.Fatal("fixture file/directory collision")
				}
				entries = append(entries, *child.leaf)
			} else {
				entries = append(entries, blockTreeEntry{name: name, kind: "tree", mode: "40000", oid: build(child)})
			}
		}
		return objectTree(t, transport, entries, width)
	}
	return build(root)
}

// fixtureCommitFiles simulates another/older writer using exact object paths.
// Git's own recursive listing supplies unchanged leaves; no index or worktree
// is manufactured to make a fixture compatible with the removed backend.
func fixtureCommitFiles(t testing.TB, repo git, files map[string][]byte, removals []string, message string) string {
	t.Helper()
	ctx := context.Background()
	if len(repo.Identity) == 0 {
		repo.Identity = []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
	}
	transport := &objectTransport{repo: repo}
	branch, err := transport.currentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	width := 40
	leaves := map[string]blockTreeEntry{}
	if branch.OID != "" {
		width = len(branch.OID)
		listing, err := readGitBytes(ctx, repo, maxCachedTreeBytes, "ls-tree", "-r", "-z", "--full-tree", branch.OID)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(listing, []byte{0}) {
			if len(line) == 0 {
				continue
			}
			header, name, ok := bytes.Cut(line, []byte{'\t'})
			fields := strings.Fields(string(header))
			if !ok || len(fields) != 3 {
				t.Fatal("invalid fixture tree listing")
			}
			leaves[string(name)] = blockTreeEntry{name: path.Base(string(name)), mode: fields[0], kind: fields[1], oid: fields[2]}
		}
	} else if format, err := repo.run(ctx, "rev-parse", "--show-object-format"); err != nil {
		t.Fatal(err)
	} else if format == "sha256" {
		width = 64
	}
	for _, removed := range removals {
		for name := range leaves {
			if name == removed || strings.HasPrefix(name, removed+"/") {
				delete(leaves, name)
			}
		}
	}
	for name, body := range files {
		leaves[name] = objectBlob(t, transport, path.Base(name), string(body), width)
	}
	tree := fixturePathTree(t, transport, leaves, width)
	var parents []string
	if branch.OID != "" {
		parents = []string{branch.OID}
	}
	commit, err := transport.writeCommit(ctx, tree, parents, message)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.advanceBranch(ctx, branch, commit); err != nil {
		t.Fatal(err)
	}
	return commit
}

func fixtureCommitEnvelopes(t testing.TB, repo git, objects []portable.EncodedEnvelope, message string) string {
	t.Helper()
	files := map[string][]byte{}
	for _, object := range objects {
		files[filepath.ToSlash(portable.MemoryRecordPath(object.Envelope))] = append(append([]byte(nil), object.Bytes...), '\n')
	}
	return fixtureCommitFiles(t, repo, files, nil, message)
}

func fixtureCorruptObject(t testing.TB, repo git, oid, kind string, body []byte) {
	t.Helper()
	name := filepath.Join(repo.Dir, "objects", oid[:2], oid[2:])
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0600); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	writer := zlib.NewWriter(file)
	_, writeErr := fmt.Fprintf(writer, "%s %d\x00%s", kind, len(body), body)
	zipErr := writer.Close()
	fileErr := file.Close()
	if writeErr != nil || zipErr != nil || fileErr != nil {
		t.Fatalf("corrupt fixture: %v %v %v", writeErr, zipErr, fileErr)
	}
}

func fixtureFetchMerge(t testing.TB, repo git, branch string) {
	t.Helper()
	ctx := context.Background()
	if _, err := repo.run(ctx, "fetch", "--quiet", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		t.Fatal(err)
	}
	transport := &objectTransport{repo: repo}
	current, err := transport.currentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := repo.run(ctx, "rev-parse", "refs/remotes/origin/"+branch)
	if err != nil {
		t.Fatal(err)
	}
	next, err := transport.mergeCommits(ctx, current.OID, remote, "fixture merge", func(ctx context.Context, base, local, remote string) error {
		for _, tree := range []string{local, remote} {
			if tree != "" {
				if _, err := transport.blockDelta(ctx, base, tree, testLineage); err != nil {
					return err
				}
			}
		}
		return nil
	}, transport.finishBlockMerge(testLineage, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.advanceBranch(ctx, current, next); err != nil {
		t.Fatal(err)
	}
}
