package roadmap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

func workspaceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitFile(t *testing.T, repo, path, content, message string) string {
	t.Helper()
	full := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", path)
	workspaceGit(t, repo, "commit", "-q", "-m", message)
	return workspaceGit(t, repo, "rev-parse", "HEAD")
}

func TestWorkspaceRoadmapTracksEachMember(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	for _, name := range []string{"api", "daemon"} {
		workspaceGit(t, ws, "init", "-q", name)
		commitFile(t, filepath.Join(ws, name), "README.md", name+"\n", "init "+name)
	}
	workspace := &project.Workspace{SchemaVersion: 1, Mode: project.ModeUnified, Name: "fleet", Root: ws,
		Members: []project.Member{{Path: "api"}, {Path: "daemon"}}}
	repository := WorkspaceRepository{Repositories: project.Repositories{Root: ws, Workspace: workspace}}

	db, err := database.Initialize(ctx, filepath.Join(t.TempDir(), "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectID, err := (activity.Store{DB: db}).EnsureProject(ctx, ws, "fleet")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	service := Service{DB: db, Root: ws, Repo: repository, Now: func() time.Time { return now }}
	node, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "fleet.wire", Title: "Wire services", Status: "active", Priority: 10, SourcePath: "api/README.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"api", "daemon/README.md"} {
		kind := "file"
		if path == "api" {
			kind = "directory"
		}
		if _, err := service.AppendBinding(ctx, Binding{ProjectID: projectID, NodeID: node.ID, Path: path, PathKind: kind, Relationship: "implementation"}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := service.RecordSync(ctx, SyncRun{ProjectID: projectID, Model: "model-a", ReviewedNodes: []string{node.ID}, ReviewedFiles: []string{"api/README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	apiHead := workspaceGit(t, filepath.Join(ws, "api"), "rev-parse", "HEAD")
	daemonHead := workspaceGit(t, filepath.Join(ws, "daemon"), "rev-parse", "HEAD")
	if run.GitCommit != "api@"+apiHead+",daemon@"+daemonHead {
		t.Fatalf("watermark = %s", run.GitCommit)
	}

	// Two commits in api change a bound file; daemon is untouched.
	commitFile(t, filepath.Join(ws, "api"), "server.go", "package api\n", "add server")
	commitFile(t, filepath.Join(ws, "api"), "README.md", "api v2\n", "update readme")
	status, err := service.Status(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Freshness != "changed" || *status.CommitsSinceSync != 2 || len(status.ChangedFiles) != 2 {
		t.Fatalf("status: freshness=%s commits=%d files=%#v", status.Freshness, *status.CommitsSinceSync, status.ChangedFiles)
	}
	for _, change := range status.ChangedFiles {
		if !strings.HasPrefix(change.Path, "api/") {
			t.Fatalf("changed path is not workspace-relative: %#v", change)
		}
	}

	// A member added after the sync counts its whole history.
	workspaceGit(t, ws, "init", "-q", "agents")
	commitFile(t, filepath.Join(ws, "agents"), "README.md", "agents\n", "init agents")
	workspace.Members = append(workspace.Members, project.Member{Path: "agents"})
	if count, err := repository.CountCommits(ctx, run.GitCommit, mustHead(t, repository)); err != nil || count != 3 {
		t.Fatalf("count with a new member = %d %v", count, err)
	}
	if dirty, err := repository.Dirty(ctx); err != nil || dirty {
		t.Fatalf("dirty = %t %v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(ws, "daemon", "scratch.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dirty, err := repository.Dirty(ctx); err != nil || !dirty {
		t.Fatalf("dirty after an untracked file = %t %v", dirty, err)
	}

	files, err := repository.Files(ctx, ".")
	if err != nil || strings.Join(files, ",") != "agents/README.md,api/README.md,api/server.go,daemon/README.md,daemon/scratch.txt" {
		t.Fatalf("files . = %v %v", files, err)
	}
	if files, err := repository.Files(ctx, "api"); err != nil || strings.Join(files, ",") != "api/README.md,api/server.go" {
		t.Fatalf("files api = %v %v", files, err)
	}
	content, state, err := repository.ContentAtRevision(ctx, run.GitCommit, "api/README.md", 1024)
	if err != nil || state != "available" || string(content) != "api\n" {
		t.Fatalf("content at watermark = %q %s %v", content, state, err)
	}
	if content, state, err := repository.ContentAtRevision(ctx, apiHead, "api/README.md", 1024); err != nil || state != "available" || string(content) != "api\n" {
		t.Fatalf("content at member commit = %q %s %v", content, state, err)
	}
	if _, state, _ := repository.ContentAtRevision(ctx, run.GitCommit, "agents/README.md", 1024); state != "reference_unavailable" {
		t.Fatalf("member absent from watermark: %s", state)
	}
	if _, state, _ := repository.ContentAtRevision(ctx, apiHead, "docs/x.md", 1024); state != "unsupported_reference" {
		t.Fatalf("path outside members: %s", state)
	}

	// Coverage compares a node's source with its recorded member commit.
	daemonNode, err := service.AppendNode(ctx, Node{ProjectID: projectID, ID: "fleet.daemon", Title: "Daemon", Status: "active", Priority: 20, SourcePath: "daemon/README.md", SourceRef: daemonHead})
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := service.Coverage(ctx, projectID, CoverageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range coverage.Nodes {
		if node.ID == daemonNode.ID && node.SourceComparison != "matches_reference" {
			t.Fatalf("daemon coverage = %#v", node)
		}
		if node.ID == "fleet.wire" && node.SourceComparison == "matches_reference" {
			t.Fatalf("a node without a source ref cannot match: %#v", node)
		}
	}
}

func mustHead(t *testing.T, repository WorkspaceRepository) string {
	t.Helper()
	head, err := repository.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return head
}
