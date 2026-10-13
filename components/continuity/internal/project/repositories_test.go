package project

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoriesSelectAndRoute(t *testing.T) {
	ws := workspaceFixture(t, ModeUnified)
	workspace, err := LoadWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	plain := Repositories{Root: filepath.Join(ws, "c")}
	if repository, err := plain.Select(""); err != nil || repository.Root != plain.Root || repository.Prefix() != "" {
		t.Fatalf("plain select: %#v %v", repository, err)
	}
	if _, err := plain.Select("a"); err == nil || !strings.Contains(err.Error(), "only in a unified workspace") {
		t.Fatalf("plain --repo: %v", err)
	}

	atFolder := Repositories{Root: ws, Workspace: workspace}
	if _, err := atFolder.Select(""); err == nil || !strings.Contains(err.Error(), "name the member repository (--repo, or repo over MCP): a, b") {
		t.Fatalf("folder default: %v", err)
	}
	if repository, err := atFolder.Select("b/"); err != nil || repository.Member != "b" || repository.Root != filepath.Join(ws, "b") || repository.Prefix() != "b/" {
		t.Fatalf("named member: %#v %v", repository, err)
	}
	if _, err := atFolder.Select("c"); err == nil || !strings.Contains(err.Error(), `"c" is not a member of workspace fleet`) {
		t.Fatalf("non-member: %v", err)
	}
	inMember := Repositories{Root: ws, Workspace: workspace, Default: "a"}
	if repository, err := inMember.Select(""); err != nil || repository.Member != "a" {
		t.Fatalf("default member: %#v %v", repository, err)
	}
	single := *workspace
	single.Members = single.Members[:1]
	if repository, err := (Repositories{Root: ws, Workspace: &single}).Select(""); err != nil || repository.Member != "a" {
		t.Fatalf("only member: %#v %v", repository, err)
	}

	if all := atFolder.All(); len(all) != 2 || all[0].Member != "a" || all[1].Member != "b" {
		t.Fatalf("all = %#v", all)
	}
	wantRoots := []string{filepath.Join(filepath.Dir(ws), "a-wt"), filepath.Join(ws, "a"), filepath.Join(ws, "b")}
	if roots := atFolder.CheckoutRoots(context.Background()); strings.Join(roots, "\n") != strings.Join(wantRoots, "\n") {
		t.Fatalf("checkout roots = %#v, want %#v", roots, wantRoots)
	}
	for _, test := range []struct {
		path, member, relative string
		ok                     bool
	}{
		{"a/cmd/main.go", "a", "cmd/main.go", true},
		{"b", "b", "", true},
		{"ab/x", "", "", false},
		{"docs/plan.md", "", "", false},
	} {
		repository, relative, ok := atFolder.ForPath(test.path)
		if ok != test.ok || repository.Member != test.member || relative != test.relative {
			t.Fatalf("ForPath(%s) = %#v %q %t", test.path, repository, relative, ok)
		}
	}
	if repository, relative, ok := plain.ForPath("x/y"); !ok || repository.Root != plain.Root || relative != "x/y" {
		t.Fatalf("plain ForPath: %#v %q %t", repository, relative, ok)
	}
}
