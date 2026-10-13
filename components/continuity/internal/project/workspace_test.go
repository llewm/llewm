package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, StateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(WorkspacePath(root), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// workspaceFixture builds a folder with members a and b, a non-member repository
// c, and a linked worktree of a outside the folder.
func workspaceFixture(t *testing.T, mode string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "ws")
	for _, name := range []string{"a", "b", "c"} {
		initRepository(t, filepath.Join(ws, name))
	}
	git(t, filepath.Join(ws, "a"), "worktree", "add", "-q", filepath.Join(base, "a-wt"))
	writeManifest(t, ws, "schema_version = 1\nmode = \""+mode+"\"\nname = \"fleet\"\n\n[[members]]\npath = \"a\"\n\n[[members]]\npath = \"b\"\n")
	return ws
}

func TestLoadWorkspaceValidatesTheManifest(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"mode", "schema_version = 1\nmode = \"shared\"\nname = \"x\"\n[[members]]\npath = \"a\"\n", "mode must be"},
		{"version", "schema_version = 2\nmode = \"unified\"\nname = \"x\"\n[[members]]\npath = \"a\"\n", "schema_version"},
		{"name", "schema_version = 1\nmode = \"unified\"\n[[members]]\npath = \"a\"\n", "name is required"},
		{"empty", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\n", "at least one member"},
		{"absolute", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\n[[members]]\npath = \"/etc\"\n", "inside the workspace"},
		{"escape", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\n[[members]]\npath = \"../a\"\n", "inside the workspace"},
		{"duplicate", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\n[[members]]\npath = \"a\"\n[[members]]\npath = \"a/\"\n", "listed twice"},
		{"nested", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\n[[members]]\npath = \"a\"\n[[members]]\npath = \"a/b\"\n", "nested inside"},
		{"unknown", "schema_version = 1\nmode = \"unified\"\nname = \"x\"\nstore = \"elsewhere\"\n[[members]]\npath = \"a\"\n", "unknown key store"},
	} {
		root := t.TempDir()
		writeManifest(t, root, test.body)
		if _, err := LoadWorkspace(root); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: err = %v, want %q", test.name, err, test.want)
		}
	}
	if workspace, err := LoadWorkspace(t.TempDir()); workspace != nil || err != nil {
		t.Fatalf("absent manifest: %v %v", workspace, err)
	}
}

func TestResolveClaimsTheWorkspaceFolderAndListedMembers(t *testing.T) {
	ws := workspaceFixture(t, ModeUnified)
	base := filepath.Dir(ws)
	for _, test := range []struct {
		start, root, kind, member string
	}{
		{ws, ws, KindWorkspace, ""},
		{filepath.Join(ws, "a"), ws, KindRepository, "a"},
		{filepath.Join(ws, "b"), ws, KindRepository, "b"},
		// A linked worktree of a member belongs to the workspace too.
		{filepath.Join(base, "a-wt"), ws, KindWorktree, "a"},
		// An unlisted repository under the folder stays its own project.
		{filepath.Join(ws, "c"), filepath.Join(ws, "c"), KindRepository, ""},
	} {
		got, err := Resolve(test.start)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", test.start, err)
		}
		if got.Root != test.root || got.Kind != test.kind || got.Member != test.member {
			t.Fatalf("Resolve(%s) = %#v, want root=%s kind=%s member=%q", test.start, got, test.root, test.kind, test.member)
		}
	}
}

func TestResolveLeavesFederatedMembersAlone(t *testing.T) {
	ws := workspaceFixture(t, ModeFederated)
	got, err := Resolve(filepath.Join(ws, "a"))
	if err != nil || got.Root != filepath.Join(ws, "a") || got.Workspace != ws || got.Member != "a" {
		t.Fatalf("federated member: %#v %v", got, err)
	}
	if _, err := Resolve(ws); err == nil || !strings.Contains(err.Error(), "federated workspace") {
		t.Fatalf("federated folder: %v", err)
	}
}

func TestResolveRefusesAFolderOfRepositoriesWithoutAManifest(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x", "y"} {
		initRepository(t, filepath.Join(base, "loose", name))
	}
	if _, err := Resolve(filepath.Join(base, "loose")); err == nil || !strings.Contains(err.Error(), "llewm workspace init") {
		t.Fatalf("loose folder: %v", err)
	}
	// The same folder inside an enclosing repository is still refused.
	initRepository(t, filepath.Join(base, "outer"))
	for _, name := range []string{"x", "y"} {
		initRepository(t, filepath.Join(base, "outer", "group", name))
	}
	if _, err := Resolve(filepath.Join(base, "outer", "group")); err == nil || !strings.Contains(err.Error(), "2 Git repositories") {
		t.Fatalf("folder inside a repository: %v", err)
	}
	if got, err := Resolve(filepath.Join(base, "loose", "x")); err != nil || got.Root != filepath.Join(base, "loose", "x") {
		t.Fatalf("repository inside the folder: %#v %v", got, err)
	}
}

func TestProposeAndWriteWorkspace(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"svc-b", "svc-a"} {
		initRepository(t, filepath.Join(base, name))
	}
	if err := os.Mkdir(filepath.Join(base, "svc-b", StateDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	proposal, warnings, err := ProposeWorkspace(base, "", ModeUnified)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Name != filepath.Base(base) || proposal.Mode != ModeUnified || len(proposal.Members) != 2 || proposal.Members[0].Path != "svc-a" || proposal.Members[1].Path != "svc-b" {
		t.Fatalf("proposal = %#v", proposal)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "svc-b has its own") {
		t.Fatalf("warnings = %v", warnings)
	}
	path, err := WriteWorkspace(proposal)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(base)
	if err != nil || loaded == nil || path != WorkspacePath(base) || len(loaded.Members) != 2 {
		t.Fatalf("round trip: %#v %v", loaded, err)
	}
	if _, err := WriteWorkspace(proposal); err == nil {
		t.Fatal("WriteWorkspace replaced an existing manifest")
	}
	if _, _, err := ProposeWorkspace(filepath.Join(base, "svc-a"), "", ModeUnified); err == nil || !strings.Contains(err.Error(), "two or more") {
		t.Fatalf("single-repository proposal: %v", err)
	}
}

func TestChangeWorkspaceModeRefusesManifestEditedAfterReview(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "schema_version = 1\nmode = \"unified\"\nname = \"fleet\"\n[[members]]\npath = \"a\"\n")
	digest, err := WorkspaceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, "schema_version = 1\nmode = \"unified\"\nname = \"renamed\"\n[[members]]\npath = \"a\"\n")
	if _, err := ChangeWorkspaceMode(root, ModeUnified, ModeFederated, digest); err == nil || !strings.Contains(err.Error(), "manifest changed since review") {
		t.Fatalf("stale manifest review: %v", err)
	}
	workspace, err := LoadWorkspace(root)
	if err != nil || workspace.Mode != ModeUnified || workspace.Name != "renamed" {
		t.Fatalf("edited manifest was overwritten: %#v %v", workspace, err)
	}
}

func TestRequireRepository(t *testing.T) {
	ws := workspaceFixture(t, ModeUnified)
	if err := RequireRepository(ws); !errors.Is(err, ErrWorkspaceRoot) {
		t.Fatalf("workspace folder: %v", err)
	}
	if err := RequireRepository(filepath.Join(ws, "a")); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := RequireRepository(t.TempDir()); err != nil {
		t.Fatalf("plain folder should be left to Git: %v", err)
	}
}
