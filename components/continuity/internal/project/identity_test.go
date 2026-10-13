package project_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

type fingerprints struct {
	commits map[string]string
	calls   int
}

func (f *fingerprints) read(_ context.Context, root string) (string, error) {
	f.calls++
	return f.commits[root], nil
}

func identityStore(t *testing.T) (*sql.DB, *fingerprints, project.Resolver) {
	t.Helper()
	db, err := database.Initialize(context.Background(), filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	commits := &fingerprints{commits: map[string]string{}}
	return db, commits, project.Resolver{DB: db, RootCommit: commits.read}
}

func locationSources(t *testing.T, db *sql.DB, projectID string) string {
	t.Helper()
	locations, err := project.Locations(context.Background(), db, projectID, 64)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for i := len(locations) - 1; i >= 0; i-- {
		parts = append(parts, locations[i].Source+":"+locations[i].Path)
	}
	return strings.Join(parts, ",")
}

func TestRegisterCreatesOpaqueStableIDs(t *testing.T) {
	ctx := context.Background()
	_, commits, resolver := identityStore(t)
	first, err := resolver.Register(ctx, "/work/a", "a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := resolver.Register(ctx, "/work/a", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	other, err := resolver.Register(ctx, "/work/b", "b")
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == other || !strings.HasPrefix(first, "project-") || len(first) != len("project-")+16 {
		t.Fatalf("ids: first=%q again=%q other=%q", first, again, other)
	}
	if commits.calls != 0 {
		t.Fatalf("Register inspected Git %d times", commits.calls)
	}
}

func TestEnsureFingerprintsOnceThenAvoidsGit(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	commits.commits["/work/a"] = "root-a"
	created, err := resolver.Ensure(ctx, "/work/a", "a", true)
	if err != nil || created.Outcome != project.OutcomeCreated {
		t.Fatalf("create: %#v %v", created, err)
	}
	calls := commits.calls
	for range 3 {
		existing, err := resolver.Ensure(ctx, "/work/a", "a", true)
		if err != nil || existing.Outcome != project.OutcomeExisting || existing.ProjectID != created.ProjectID || existing.RepositoryID != created.RepositoryID {
			t.Fatalf("existing: %#v %v", existing, err)
		}
	}
	if commits.calls != calls {
		t.Fatalf("fingerprinted location called Git %d more times", commits.calls-calls)
	}
	if got := locationSources(t, db, created.ProjectID); got != "observed:/work/a" {
		t.Fatalf("locations = %s", got)
	}
}

func TestEnsureObservesFingerprintForBackfilledLocation(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Register models a pre-038 project: a location with no known root commit.
	id, err := resolver.Register(ctx, checkout, "a")
	if err != nil {
		t.Fatal(err)
	}
	commits.commits[checkout] = "root-a"
	if _, err := resolver.Ensure(ctx, checkout, "a", true); err != nil {
		t.Fatal(err)
	}
	locations, err := project.Locations(ctx, db, id, 64)
	if err != nil || len(locations) != 2 || locations[0].RootCommit != "root-a" {
		t.Fatalf("locations = %#v %v", locations, err)
	}
}

func TestEnsureRelocatesWhenTheStoreMovedWithTheCheckout(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	commits.commits["/old/a"] = "root-a"
	created, err := resolver.Ensure(ctx, "/old/a", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	commits.commits["/new/a"] = "root-a"
	lookup, err := resolver.Lookup(ctx, "/new/a", true)
	if err != nil || lookup.Outcome != project.OutcomeRelocationPending || lookup.ProjectID != created.ProjectID || lookup.PreviousRoot != "/old/a" {
		t.Fatalf("read-only lookup: %#v %v", lookup, err)
	}
	if got := locationSources(t, db, created.ProjectID); got != "observed:/old/a" {
		t.Fatalf("read-only lookup wrote locations: %s", got)
	}
	moved, err := resolver.Ensure(ctx, "/new/a", "a", true)
	if err != nil || moved.Outcome != project.OutcomeRelocated || moved.ProjectID != created.ProjectID || moved.PreviousRoot != "/old/a" {
		t.Fatalf("relocation: %#v %v", moved, err)
	}
	var root string
	if err := db.QueryRowContext(ctx, `SELECT root FROM projects WHERE id=?`, created.ProjectID).Scan(&root); err != nil || root != "/new/a" {
		t.Fatalf("root = %q %v", root, err)
	}
	if got := locationSources(t, db, created.ProjectID); got != "observed:/old/a,relocated:/new/a" {
		t.Fatalf("locations = %s", got)
	}
}

func TestEnsureRelocatesAnUnfingerprintedProjectWhoseOldRootIsGone(t *testing.T) {
	ctx := context.Background()
	_, commits, resolver := identityStore(t)
	gone := filepath.Join(t.TempDir(), "moved-away")
	id, err := resolver.Register(ctx, gone, "a")
	if err != nil {
		t.Fatal(err)
	}
	commits.commits["/new/a"] = "root-a"
	moved, err := resolver.Ensure(ctx, "/new/a", "a", true)
	if err != nil || moved.Outcome != project.OutcomeRelocated || moved.ProjectID != id {
		t.Fatalf("relocation: %#v %v", moved, err)
	}
}

func TestEnsureRefusesToGuessInsideAStoreWithUnrelatedProjects(t *testing.T) {
	ctx := context.Background()
	_, commits, resolver := identityStore(t)
	existing := t.TempDir()
	commits.commits[existing] = "root-a"
	if _, err := resolver.Ensure(ctx, existing, "a", true); err != nil {
		t.Fatal(err)
	}
	commits.commits["/new/b"] = "root-b"
	_, err := resolver.Ensure(ctx, "/new/b", "b", true)
	var rebind *project.RebindRequiredError
	if !errors.As(err, &rebind) || len(rebind.Candidates) != 1 || !strings.Contains(err.Error(), "rebind --new") {
		t.Fatalf("expected rebind requirement, got %v", err)
	}
}

func TestSharedStoreRequiresExplicitRebindForAMatchingRepository(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	commits.commits["/work/a"] = "root-a"
	original, err := resolver.Ensure(ctx, "/work/a", "a", false)
	if err != nil {
		t.Fatal(err)
	}
	commits.commits["/clone/a"] = "root-a"
	_, err = resolver.Ensure(ctx, "/clone/a", "a", false)
	var rebind *project.RebindRequiredError
	if !errors.As(err, &rebind) || rebind.Candidates[0].ProjectID != original.ProjectID || rebind.Candidates[0].Root != "/work/a" {
		t.Fatalf("expected rebind requirement, got %v", err)
	}
	if _, err := resolver.Lookup(ctx, "/clone/a", false); !errors.As(err, &rebind) {
		t.Fatalf("lookup did not require rebind: %v", err)
	}

	separate, err := resolver.Rebind(ctx, "/clone/a", "a clone", "")
	if err != nil || separate.Outcome != project.OutcomeCreated || separate.ProjectID == original.ProjectID {
		t.Fatalf("rebind --new: %#v %v", separate, err)
	}
	if _, err := resolver.Rebind(ctx, "/clone/a", "a", original.ProjectID); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("rebind onto a held root: %v", err)
	}

	commits.commits["/moved/a"] = "root-a"
	moved, err := resolver.Rebind(ctx, "/moved/a", "a", original.ProjectID)
	if err != nil || moved.Outcome != project.OutcomeRebound || moved.PreviousRoot != "/work/a" {
		t.Fatalf("rebind: %#v %v", moved, err)
	}
	if got := locationSources(t, db, original.ProjectID); got != "observed:/work/a,rebound:/moved/a" {
		t.Fatalf("locations = %s", got)
	}
	again, err := resolver.Ensure(ctx, "/moved/a", "a", false)
	if err != nil || again.Outcome != project.OutcomeExisting || again.ProjectID != original.ProjectID {
		t.Fatalf("after rebind: %#v %v", again, err)
	}
	if _, err := resolver.Rebind(ctx, "/x", "x", "project-unknown"); err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("rebind to unknown project: %v", err)
	}
}

func TestSharedStoreCreatesProjectsForUnrelatedCheckouts(t *testing.T) {
	ctx := context.Background()
	_, commits, resolver := identityStore(t)
	commits.commits["/work/a"] = "root-a"
	commits.commits["/work/b"] = "root-b"
	a, err := resolver.Ensure(ctx, "/work/a", "a", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resolver.Ensure(ctx, "/work/b", "b", false)
	if err != nil || b.Outcome != project.OutcomeCreated || b.ProjectID == a.ProjectID {
		t.Fatalf("second project: %#v %v", b, err)
	}
	if _, err := resolver.Lookup(ctx, "/work/c", false); !errors.Is(err, project.ErrNotRegistered) {
		t.Fatalf("unknown lookup: %v", err)
	}
}

func TestSymlinkedSpellingIsNotAMove(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	commits.commits[real] = "root-a"
	commits.commits[link] = "root-a"
	created, err := resolver.Ensure(ctx, real, "a", true)
	if err != nil {
		t.Fatal(err)
	}
	via, err := resolver.Ensure(ctx, link, "a", true)
	if err != nil || via.Outcome != project.OutcomeExisting || via.ProjectID != created.ProjectID {
		t.Fatalf("symlinked open: %#v %v", via, err)
	}
	if got := locationSources(t, db, created.ProjectID); got != "observed:"+real {
		t.Fatalf("symlinked spelling was recorded as a location: %s", got)
	}
}

func TestConcurrentFirstOpensShareOneProject(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	commits.commits["/work/a"] = "root-a"
	const openers = 8
	results := make(chan project.Resolution, openers)
	errs := make(chan error, openers)
	for range openers {
		go func() {
			result, err := resolver.Ensure(ctx, "/work/a", "a", true)
			results <- result
			errs <- err
		}()
	}
	ids := map[string]bool{}
	for range openers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		ids[(<-results).ProjectID] = true
	}
	var projects int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil || len(ids) != 1 || projects != 1 {
		t.Fatalf("concurrent first opens: ids=%v projects=%d %v", ids, projects, err)
	}
}

func TestGitRootCommit(t *testing.T) {
	ctx := context.Background()
	empty := t.TempDir()
	runGit(t, empty, "init", "-q")
	if commit, err := project.GitRootCommit(ctx, empty); err != nil || commit != "" {
		t.Fatalf("empty repository: %q %v", commit, err)
	}
	runGit(t, empty, "commit", "-q", "--allow-empty", "-m", "first")
	runGit(t, empty, "commit", "-q", "--allow-empty", "-m", "second")
	commit, err := project.GitRootCommit(ctx, empty)
	if err != nil || len(commit) != 40 {
		t.Fatalf("root commit: %q %v", commit, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestSyncMembersRecordsEachMemberOnce(t *testing.T) {
	ctx := context.Background()
	db, commits, resolver := identityStore(t)
	ws := t.TempDir()
	for _, name := range []string{"api", "daemon"} {
		if err := os.MkdirAll(filepath.Join(ws, name, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		commits.commits[filepath.Join(ws, name)] = "root-" + name
	}
	workspace := project.Workspace{SchemaVersion: 1, Mode: project.ModeUnified, Name: "fleet", Root: ws,
		Members: []project.Member{{Path: "api"}, {Path: "daemon"}, {Path: "agents"}}}
	created, err := resolver.Ensure(ctx, ws, "fleet", true)
	if err != nil || created.Outcome != project.OutcomeCreated {
		t.Fatalf("workspace project: %#v %v", created, err)
	}
	if err := resolver.SyncMembers(ctx, created.ProjectID, workspace); err != nil {
		t.Fatal(err)
	}
	calls := commits.calls
	if err := resolver.SyncMembers(ctx, created.ProjectID, workspace); err != nil {
		t.Fatal(err)
	}
	if commits.calls != calls {
		t.Fatalf("an unchanged workspace called Git %d more times", commits.calls-calls)
	}
	// The workspace folder has no fingerprint, yet reopening it is write-free.
	again, err := resolver.Ensure(ctx, ws, "fleet", true)
	if err != nil || again.Outcome != project.OutcomeExisting || commits.calls != calls {
		t.Fatalf("reopen: %#v %v calls=%d", again, err, commits.calls-calls)
	}
	members, err := project.Members(ctx, db, created.ProjectID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, member := range members {
		got = append(got, fmt.Sprintf("%s:%t:%t:%s", member.Path, member.Present, member.RepositoryID != "", member.RootCommit))
	}
	if strings.Join(got, ",") != "api:true:true:root-api,daemon:true:true:root-daemon,agents:false:false:" {
		t.Fatalf("members = %v", got)
	}
	// Member fingerprints never make an unrelated checkout look like the workspace.
	commits.commits["/clone/api"] = "root-api"
	if other, err := resolver.Ensure(ctx, "/clone/api", "api", false); err != nil || other.Outcome != project.OutcomeCreated {
		t.Fatalf("clone of a member: %#v %v", other, err)
	}
	if got := locationSources(t, db, created.ProjectID); got != "observed:"+ws {
		t.Fatalf("project locations include members: %s", got)
	}
}
