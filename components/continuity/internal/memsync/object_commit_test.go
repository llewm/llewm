package memsync

import (
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectCommitPreservesDivergentParentsWithoutMovingRefs(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			transport := objectTransportFixture(t, format)
			transport.repo.Identity = []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}
			width := 40
			if format == "sha256" {
				width = 64
			}
			baseTree := objectTree(t, transport, nil, width)
			base, err := transport.writeCommit(ctx, baseTree, nil, "base\n")
			if err != nil {
				t.Fatal(err)
			}
			localTree := objectTree(t, transport, []blockTreeEntry{objectBlob(t, transport, "local", "local bytes", width)}, width)
			remoteTree := objectTree(t, transport, []blockTreeEntry{objectBlob(t, transport, "remote", "remote bytes", width)}, width)
			local, err := transport.writeCommit(ctx, localTree, []string{base}, "local\n")
			if err != nil {
				t.Fatal(err)
			}
			remote, err := transport.writeCommit(ctx, remoteTree, []string{base}, "remote\n")
			if err != nil {
				t.Fatal(err)
			}
			mergedTree, err := transport.mergeTrees(ctx, baseTree, localTree, remoteTree)
			if err != nil {
				t.Fatal(err)
			}
			validated := 0
			validate := func(_ context.Context, b, l, r string) error {
				validated++
				if b != baseTree || l != localTree || r != remoteTree {
					return fmt.Errorf("wrong policy inputs: %s %s %s", b, l, r)
				}
				return nil
			}
			merged, err := transport.mergeCommits(ctx, local, remote, "merge immutable history\n", validate, nil)
			if err != nil {
				t.Fatal(err)
			}
			if validated != 1 {
				t.Fatal("merge bypassed namespace validation")
			}
			commit, err := transport.readCommit(ctx, merged)
			if err != nil || commit.Tree != mergedTree || strings.Join(commit.Parents, " ") != local+" "+remote {
				t.Fatalf("merge lost its parents: %+v %v", commit, err)
			}
			for _, ancestor := range []string{base, local, remote} {
				if _, err := transport.repo.run(ctx, "merge-base", "--is-ancestor", ancestor, merged); err != nil {
					t.Fatalf("history was rewritten: %v", err)
				}
			}
			if refs, err := transport.repo.run(ctx, "for-each-ref", "--format=%(refname)"); err != nil || refs != "" {
				t.Fatalf("commit construction changed refs: %q %v", refs, err)
			}
			for _, pair := range [][2]string{{"", remote}, {base, remote}, {merged, remote}, {remote, remote}, {local, remote}} {
				policyErr := errors.New("fixture namespace rejected")
				called := false
				_, err := transport.mergeCommits(ctx, pair[0], pair[1], "must refuse", func(context.Context, string, string, string) error {
					called = true
					return policyErr
				}, nil)
				if !called || !errors.Is(err, policyErr) {
					t.Fatalf("merge or fast-forward bypassed rejection: %v", err)
				}
			}
			allow := func(context.Context, string, string, string) error { return nil }
			for _, tc := range []struct{ local, remote, want string }{{"", remote, remote}, {base, remote, remote}, {merged, remote, merged}, {remote, remote, remote}} {
				got, err := transport.mergeCommits(ctx, tc.local, tc.remote, "no merge needed", allow, nil)
				if err != nil || got != tc.want {
					t.Fatalf("fast-forward/ancestor result: %s want %s: %v", got, tc.want, err)
				}
			}
			unrelatedTree := objectTree(t, transport, []blockTreeEntry{objectBlob(t, transport, "unrelated", "other initial writer", width)}, width)
			unrelated, err := transport.writeCommit(ctx, unrelatedTree, nil, "other initial history")
			if err != nil {
				t.Fatal(err)
			}
			joined, err := transport.mergeCommits(ctx, local, unrelated, "join initial histories", func(_ context.Context, b, l, r string) error {
				if b != "" || l != localTree || r != unrelatedTree {
					return errors.New("incorrect unrelated-history policy inputs")
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			joinedCommit, err := transport.readCommit(ctx, joined)
			if err != nil || strings.Join(joinedCommit.Parents, " ") != local+" "+unrelated {
				t.Fatalf("unrelated history lost a parent: %+v %v", joinedCommit, err)
			}
			// Criss-cross histories have two equally valid merge bases. Do not
			// choose one silently or invoke policy with an arbitrary base.
			reverse, err := transport.writeCommit(ctx, mergedTree, []string{remote, local}, "reverse merge")
			if err != nil {
				t.Fatal(err)
			}
			called := false
			if _, err := transport.mergeCommits(ctx, merged, reverse, "ambiguous", func(context.Context, string, string, string) error {
				called = true
				return nil
			}, nil); err == nil || called {
				t.Fatalf("ambiguous base accepted or policy received arbitrary base: %v", err)
			}
			if _, err := transport.repo.run(ctx, "fsck", "--strict", "--no-reflogs", merged); err != nil {
				t.Fatal(err)
			}
			unborn, err := transport.currentBranch(ctx)
			if err != nil || unborn.OID != "" {
				t.Fatalf("unborn branch: %+v %v", unborn, err)
			}
			if err := transport.advanceBranch(ctx, unborn, base); err != nil {
				t.Fatal(err)
			}
			current, err := transport.currentBranch(ctx)
			if err != nil || current.OID != base {
				t.Fatalf("initial publication: %+v %v", current, err)
			}
			// Advancing the resolved target preserves a symbolic HEAD chain.
			if _, err := transport.repo.run(ctx, "symbolic-ref", "refs/heads/alias", current.Ref); err != nil {
				t.Fatal(err)
			}
			if _, err := transport.repo.run(ctx, "symbolic-ref", "HEAD", "refs/heads/alias"); err != nil {
				t.Fatal(err)
			}
			if err := transport.advanceBranch(ctx, current, merged); err != nil {
				t.Fatal(err)
			}
			if head, err := transport.repo.run(ctx, "symbolic-ref", "--no-recurse", "HEAD"); err != nil || head != "refs/heads/alias" {
				t.Fatalf("publication collapsed HEAD chain: %q %v", head, err)
			}
			if err := transport.advanceBranch(ctx, current, local); err == nil {
				t.Fatal("stale expected tip accepted")
			}
			current.OID = merged
			if err := transport.advanceBranch(ctx, current, remote); err == nil {
				t.Fatal("non-fast-forward history rewrite accepted")
			}
			after, err := transport.currentBranch(ctx)
			if err != nil || after != current {
				t.Fatalf("refusal changed history: %+v %v", after, err)
			}
			if _, err := os.Lstat(filepath.Join(transport.repo.Dir, "index")); !os.IsNotExist(err) {
				t.Fatalf("bare commit created an index: %v", err)
			}
		})
	}
}

func TestObjectCommitValidatesHeadersAndExactIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	transport := objectTransportFixture(t, "sha1")
	tree := objectTree(t, transport, nil, 40)
	headers := "tree " + tree + "\nauthor Fixture <fixture@example.invalid> 1 +0000\ncommitter Fixture <fixture@example.invalid> 1 +0000"
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"signed", headers + "\ngpgsig fixture\n continuation\n\nmessage\n", true},
		{"missing-tree", "author Fixture\ncommitter Fixture\n\nmessage", false},
		{"duplicate-tree", headers + "\ntree " + tree + "\n\n", false},
		{"wrong-width", strings.Replace(headers, tree, strings.Repeat("b", 64), 1) + "\n\n", false},
		{"bad-parent", headers + "\nparent refs/heads/main\n\n", false},
		{"missing-author", "tree " + tree + "\ncommitter Fixture\n\n", false},
		{"identity-continuation", headers + "\n continuation\n\n", false},
		{"oversize", headers + "\n\n" + strings.Repeat("x", maxTransportCommitBytes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oid, err := gitObjectIdentity("commit", []byte(tc.body), 40)
			if err != nil {
				t.Fatal(err)
			}
			if err := transport.repo.runInput(ctx, tc.body, "hash-object", "--literally", "-w", "-t", "commit", "--stdin"); err != nil {
				t.Fatal(err)
			}
			_, err = transport.readCommit(ctx, oid)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	// A loose object stored under another object's name must not supply a tree.
	body := []byte(headers + "\n\noriginal\n")
	oid, _ := gitObjectIdentity("commit", body, 40)
	path := filepath.Join(transport.repo.Dir, "objects", oid[:2], oid[2:])
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(headers + "\n\nchanged\n")
	writer := zlib.NewWriter(file)
	_, writeErr := fmt.Fprintf(writer, "commit %d\x00%s", len(corrupt), corrupt)
	closeErr := writer.Close()
	fileErr := file.Close()
	if writeErr != nil || closeErr != nil || fileErr != nil {
		t.Fatalf("write fixture: %v %v %v", writeErr, closeErr, fileErr)
	}
	if _, err := transport.readCommit(ctx, oid); err == nil {
		t.Fatal("commit with mismatched bytes was accepted")
	}
}
