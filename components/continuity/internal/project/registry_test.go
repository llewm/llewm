package project

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/symlinktest"
)

func TestRegistryUpgradeKeepsOldWritersAndDistinctCheckouts(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	db, err := sql.Open("sqlite", registryDSN(filepath.Join(global, RegistryFileName), false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE repository_routes(repository_id TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,project_id TEXT NOT NULL,root_path TEXT NOT NULL,store_path TEXT NOT NULL,observed_at TEXT NOT NULL); INSERT INTO repository_routes VALUES('repo','hash','project','old','store','before');`)
	if err != nil {
		t.Fatal(err)
	}
	entry := RegistryEntry{RepositoryID: "repo", Fingerprint: "hash", ProjectID: "project", Root: filepath.Join(global, "checkout"), Store: filepath.Join(global, "store"), ObservedAt: "first"}
	if err = ObserveRegistry(ctx, global, entry); err != nil {
		t.Fatal(err)
	}
	// The old binary still writes its original table; compatibility triggers retain routes.
	if _, err = db.Exec(`UPDATE repository_routes SET root_path='third',observed_at='old-writer' WHERE repository_id='repo'`); err != nil {
		t.Fatal(err)
	}
	all, err := RegistryAll(ctx, global)
	if err != nil || len(all) != 3 {
		t.Fatalf("routes=%+v %v", all, err)
	}
	entry.ObservedAt = "second"
	if err = ObserveRegistry(ctx, global, entry); err != nil {
		t.Fatal(err)
	}
	// Reopening restores the legacy pointer after another checkout was observed;
	// only a subsequent identical observation is a true no-op.
	entry.ObservedAt = "third"
	if err = ObserveRegistry(ctx, global, entry); err != nil {
		t.Fatal(err)
	}
	var observed string
	if err = db.QueryRow(`SELECT observed_at FROM checkout_routes WHERE root_path=?`, entry.Root).Scan(&observed); err != nil || observed != "second" {
		t.Fatalf("unchanged route was written: %q %v", observed, err)
	}
	if _, err = db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	if err = ObserveRegistry(ctx, global, entry); err == nil {
		t.Fatal("accepted newer registry")
	}
}

func TestRegistryGroupsKeepCheckoutsSharingRepositoryAndStore(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	base := t.TempDir()
	for _, group := range []string{"one", "two"} {
		e := RegistryEntry{RepositoryID: "shared-repo", ProjectID: "shared-project", Fingerprint: "hash", GroupRoot: filepath.Join(base, group), Member: "member", Root: filepath.Join(base, group, "member"), Store: filepath.Join(base, "shared.db")}
		if err := ObserveRegistry(ctx, global, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []string{"one", "two"} {
		rows, err := RegistryGroup(ctx, global, filepath.Join(base, group))
		if err != nil || len(rows) != 1 || rows[0].Root != filepath.Join(base, group, "member") {
			t.Fatalf("group %s routes=%+v %v", group, rows, err)
		}
	}
}

func TestRegistryBusyIsBoundedAndUnchangedObservationNeedsNoWriter(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	e := RegistryEntry{RepositoryID: "repo", Fingerprint: "hash", ProjectID: "project", Root: global, Store: filepath.Join(global, "store")}
	if err := ObserveRegistry(ctx, global, e); err != nil {
		t.Fatal(err)
	}
	db, err := openRegistry(global)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	if err = ObserveRegistry(ctx, global, e); err != nil {
		t.Fatalf("unchanged observation requires writer: %v", err)
	}
	e.LineageID = "new"
	start := time.Now()
	if err = ObserveRegistry(ctx, global, e); err == nil {
		t.Fatal("expected lock contention")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("registry stalled project open")
	}
}

func TestRegistryPruneRequiresReviewedAbsentCheckoutAndStore(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	base := t.TempDir()
	for _, name := range []string{"stale", "retained"} {
		e := RegistryEntry{RepositoryID: name, Fingerprint: "hash", ProjectID: name, Root: filepath.Join(base, name), Store: filepath.Join(base, name+".db")}
		if name == "retained" {
			if err := os.WriteFile(e.Store, []byte("project bytes"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := ObserveRegistry(ctx, global, e); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := PruneRegistry(ctx, global, "")
	if err != nil || preview.Applied || len(preview.Candidates) != 1 {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	if _, err = PruneRegistry(ctx, global, "unreviewed"); err == nil {
		t.Fatal("accepted wrong review")
	}
	applied, err := PruneRegistry(ctx, global, preview.ReviewSHA256)
	if err != nil || !applied.Applied {
		t.Fatalf("apply=%+v %v", applied, err)
	}
	rows, err := RegistryAll(ctx, global)
	if err != nil || len(rows) != 1 || rows[0].RepositoryID != "retained" {
		t.Fatalf("remaining=%+v %v", rows, err)
	}
	if got, err := os.ReadFile(filepath.Join(base, "retained.db")); err != nil || string(got) != "project bytes" {
		t.Fatal("prune changed project data")
	}
	if PathAvailability("relative/offline") != "unknown" {
		t.Fatal("unresolved path treated as deleted")
	}
}

func TestRegistryRoutesFederatedMembersWithoutProjectBodies(t *testing.T) {
	ctx := context.Background()
	global := t.TempDir()
	// Registry routes record canonical roots; macOS reaches t.TempDir through /var.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	group := filepath.Join(base, "fleet")
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
	symlinktest.Create(t, group, link)
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
