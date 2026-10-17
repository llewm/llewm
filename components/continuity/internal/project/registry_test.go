package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRoutesFederatedMembersWithoutProjectBodies(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	group := filepath.Join(t.TempDir(), "fleet")
	for _, dir := range []string{"a", "b", "a-moved"} {
		if err := os.MkdirAll(filepath.Join(group, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspace := Workspace{Mode: ModeFederated, Name: "fleet", Root: group, Members: []Member{{Path: "a"}, {Path: "b"}}}
	entries := []RegistryEntry{
		{Fingerprint: "root-b", RepositoryID: "repo-b", ProjectID: "project-b", LineageID: "lineage:b", Root: filepath.Join(group, "b"), Store: filepath.Join(group, "b", ".llewm/continuity", "continuity.db"), GroupRoot: group, GroupName: "fleet", Member: "b"},
		{Fingerprint: "root-a", RepositoryID: "repo-a", ProjectID: "project-a", LineageID: "lineage:a", Root: filepath.Join(group, "a"), Store: filepath.Join(group, "a", ".llewm/continuity", "continuity.db"), GroupRoot: group, GroupName: "fleet", Member: "a"},
	}
	for _, entry := range entries {
		if err := ObserveRegistry(ctx, global, entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := RegistryGroup(ctx, global, group)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Member != "a" || got[1].Member != "b" || got[0].Store != entries[1].Store {
		t.Fatalf("routes = %#v", got)
	}
	entries[1].Root = filepath.Join(group, "a-moved")
	if err := ObserveRegistry(ctx, global, entries[1]); err != nil {
		t.Fatal(err)
	}
	got, err = RegistryGroup(ctx, global, group)
	if err != nil || len(got) != 2 || got[0].Root != entries[1].Root {
		t.Fatalf("updated routes = %#v, %v", got, err)
	}
	resolved, err := ResolveRegistryReference(ctx, global, workspace, "lineage:b")
	if err != nil || resolved.ProjectID != "project-b" {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	if _, err := ResolveRegistryReference(ctx, global, workspace, "missing"); err == nil {
		t.Fatal("missing reference resolved")
	}
}

func TestWorkspaceRoutesFollowManifestDiskAndSymlinks(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	base := t.TempDir()
	group := filepath.Join(base, "fleet")
	for _, dir := range []string{"a", "b", "c"} {
		if err := os.MkdirAll(filepath.Join(group, dir), 0o700); err != nil {
			t.Fatal(err)
		}
		entry := RegistryEntry{Fingerprint: "root-" + dir, RepositoryID: "repo-" + dir, ProjectID: "project-" + dir, Root: filepath.Join(group, dir), Store: filepath.Join(group, dir, "store.db"), GroupRoot: group, GroupName: "fleet", Member: dir}
		if err := ObserveRegistry(ctx, global, entry); err != nil {
			t.Fatal(err)
		}
	}
	// A symlinked path to the workspace must reach the same routes.
	link := filepath.Join(base, "link")
	if err := os.Symlink(group, link); err != nil {
		t.Fatal(err)
	}
	// c left the manifest; b's checkout was removed from disk.
	if err := os.RemoveAll(filepath.Join(group, "b")); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{Mode: ModeFederated, Name: "fleet", Root: link, Members: []Member{{Path: "a"}, {Path: "b"}}}
	routes, err := WorkspaceRoutes(ctx, global, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].Member != "a" || routes[0].Missing || routes[1].Member != "b" || !routes[1].Missing {
		t.Fatalf("routes = %#v", routes)
	}
	if _, err := ResolveRegistryReference(ctx, global, workspace, "a"); err != nil {
		t.Fatalf("resolve a through symlink: %v", err)
	}
	if _, err := ResolveRegistryReference(ctx, global, workspace, "b"); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("missing checkout resolved: %v", err)
	}
	if _, err := ResolveRegistryReference(ctx, global, workspace, "c"); err == nil {
		t.Fatal("member removed from the manifest resolved")
	}
}
