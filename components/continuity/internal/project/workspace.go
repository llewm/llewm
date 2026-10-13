package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// WorkspaceFileName is the manifest inside a workspace folder's state directory.
const WorkspaceFileName = "workspace.toml"

// Workspace modes.
const (
	ModeUnified   = "unified"
	ModeFederated = "federated"
)

// KindWorkspace is a checkout resolved at a workspace folder outside any member.
const KindWorkspace = "workspace"

// Workspace is a manifest that groups several repositories under one folder.
// Membership is explicit: a repository under the folder that is not listed stays
// its own project.
type Workspace struct {
	SchemaVersion int      `toml:"schema_version" json:"schema_version"`
	Mode          string   `toml:"mode" json:"mode"`
	Name          string   `toml:"name" json:"name"`
	Members       []Member `toml:"members" json:"members"`
	// Root is the workspace folder; it is derived from the manifest location.
	Root string `toml:"-" json:"root"`
}

// Member is one repository in a workspace, relative to the workspace folder.
type Member struct {
	Path string `toml:"path" json:"path"`
}

// WorkspacePath returns the manifest path for a workspace folder.
func WorkspacePath(root string) string {
	return filepath.Join(root, StateDirName, WorkspaceFileName)
}

// LoadWorkspace reads and validates the manifest in root, or returns nil when
// root has none.
func LoadWorkspace(root string) (*Workspace, error) {
	workspace, _, err := LoadWorkspaceWithDigest(root)
	return workspace, err
}

// LoadWorkspaceWithDigest reads and validates one manifest snapshot and returns
// the digest of those exact bytes.
func LoadWorkspaceWithDigest(root string) (*Workspace, string, error) {
	path := WorkspacePath(root)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var workspace Workspace
	metadata, err := toml.Decode(string(content), &workspace)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return nil, "", fmt.Errorf("%s: unknown key %s", path, undecoded[0])
	}
	workspace.Root = root
	if err := workspace.Validate(); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	digest := sha256.Sum256(content)
	return &workspace, hex.EncodeToString(digest[:]), nil
}

// Validate checks the manifest shape. Member paths must be distinct, relative,
// inside the workspace folder, and not nested in one another.
func (w Workspace) Validate() error {
	if w.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", w.SchemaVersion)
	}
	if w.Mode != ModeUnified && w.Mode != ModeFederated {
		return fmt.Errorf("mode must be %q or %q", ModeUnified, ModeFederated)
	}
	if strings.TrimSpace(w.Name) == "" {
		return errors.New("name is required")
	}
	if len(w.Members) == 0 {
		return errors.New("at least one member is required")
	}
	seen := map[string]bool{}
	for _, member := range w.Members {
		path := filepath.Clean(filepath.FromSlash(member.Path))
		if member.Path == "" || !filepath.IsLocal(path) || path == StateDirName {
			return fmt.Errorf("member path %q must be a directory inside the workspace folder", member.Path)
		}
		if seen[path] {
			return fmt.Errorf("member path %q is listed twice", member.Path)
		}
		seen[path] = true
	}
	for a := range seen {
		for b := range seen {
			if a != b && within(filepath.Join("/", b), filepath.Join("/", a)) {
				return fmt.Errorf("member %q is nested inside member %q", b, a)
			}
		}
	}
	return nil
}

// MemberAt returns the member whose checkout is dir.
func (w Workspace) MemberAt(dir string) (Member, bool) {
	for _, member := range w.Members {
		path := w.MemberRoot(member)
		if path == dir || samePath(path, dir) {
			return member, true
		}
	}
	return Member{}, false
}

// MemberRoot returns a member's absolute checkout path.
func (w Workspace) MemberRoot(member Member) string {
	return filepath.Join(w.Root, filepath.FromSlash(member.Path))
}

// findWorkspace returns the nearest manifest at or above start.
func findWorkspace(start string) (*Workspace, error) {
	current := start
	for {
		workspace, err := LoadWorkspace(current)
		if err != nil || workspace != nil {
			return workspace, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, nil
		}
		current = parent
	}
}

// Resolve finds the project root for start. A unified workspace manifest claims
// its folder and every listed member checkout (including their worktrees and
// submodules); otherwise the Git checkout rules of ResolveCheckout apply.
func Resolve(start string) (Checkout, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Checkout{}, err
	}
	workspace, err := findWorkspace(abs)
	if err != nil {
		return Checkout{}, err
	}
	gitRoot, gitErr := FindRoot(abs)
	if workspace != nil && (gitErr != nil || within(workspace.Root, gitRoot)) {
		// start is in the workspace folder but not inside any member checkout.
		if workspace.Mode != ModeUnified {
			return Checkout{}, fmt.Errorf("%s is a federated workspace; each member keeps its own project, so run this inside a member", workspace.Root)
		}
		return Checkout{Root: workspace.Root, Path: workspace.Root, Kind: KindWorkspace, Workspace: workspace.Root}, nil
	}
	// A folder that is not itself a checkout but holds several is a workspace
	// waiting for a manifest, even when some enclosing directory is a repository.
	if gitErr != nil || gitRoot != abs {
		if repositories, _ := DiscoverRepositories(abs); len(repositories) > 1 {
			return Checkout{}, fmt.Errorf("%s holds %d Git repositories but no workspace manifest; run `llewm workspace init` to group them, or run inside one repository", abs, len(repositories))
		}
	}
	if gitErr != nil {
		return Checkout{}, gitErr
	}
	checkout, err := ResolveCheckout(abs)
	if err != nil {
		return Checkout{}, err
	}
	if checkout.Root != checkout.Path {
		// A worktree or submodule may live outside the folder that lists its owner.
		if workspace, err = findWorkspace(checkout.Root); err != nil {
			return Checkout{}, err
		}
	}
	if workspace != nil {
		if member, ok := workspace.MemberAt(checkout.Root); ok {
			checkout.Workspace, checkout.Member = workspace.Root, member.Path
			if workspace.Mode == ModeUnified {
				checkout.Root = workspace.Root
			}
		}
	}
	return checkout, nil
}

// DiscoverRepositories lists the immediate subdirectories of dir that are Git
// checkouts, sorted by name.
func DiscoverRepositories(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var repositories []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), ".git")); err == nil {
			repositories = append(repositories, entry.Name())
		}
	}
	sort.Strings(repositories)
	return repositories, nil
}

// ProposeWorkspace builds a manifest for the repositories directly under dir.
// Warnings name member stores that a unified workspace would leave behind.
func ProposeWorkspace(dir, name, mode string) (Workspace, []string, error) {
	repositories, err := DiscoverRepositories(dir)
	if err != nil {
		return Workspace{}, nil, err
	}
	if len(repositories) < 2 {
		return Workspace{}, nil, fmt.Errorf("%s holds %d Git repositories; a workspace groups two or more", dir, len(repositories))
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if mode == "" {
		mode = ModeUnified
	}
	workspace := Workspace{SchemaVersion: 1, Mode: mode, Name: name, Root: dir}
	var warnings []string
	for _, repository := range repositories {
		workspace.Members = append(workspace.Members, Member{Path: repository})
		if mode == ModeUnified && hasStateDir(filepath.Join(dir, repository)) {
			warnings = append(warnings, fmt.Sprintf("%s has its own %s store; as a member it will use the workspace store instead, and its existing history is not merged", repository, StateDirName))
		}
	}
	return workspace, warnings, workspace.Validate()
}

// Encode renders the manifest as TOML.
func (w Workspace) Encode() ([]byte, error) {
	var buffer bytes.Buffer
	err := toml.NewEncoder(&buffer).Encode(w)
	return buffer.Bytes(), err
}

// WriteWorkspace writes a new manifest; it never replaces an existing one.
func WriteWorkspace(w Workspace) (string, error) {
	if err := w.Validate(); err != nil {
		return "", err
	}
	content, err := w.Encode()
	if err != nil {
		return "", err
	}
	path := WorkspacePath(w.Root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return "", err
	}
	return path, file.Close()
}

// WorkspaceDigest returns a content digest for the manifest currently on disk.
// Reviewed topology changes use it to detect edits made while records are moving.
func WorkspaceDigest(root string) (string, error) {
	content, err := os.ReadFile(WorkspacePath(root))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

// ChangeWorkspaceMode atomically replaces only the mode of an existing
// workspace manifest. The caller supplies the manifest digest it reviewed so a
// concurrent edit is refused instead of overwritten.
func ChangeWorkspaceMode(root, from, to, reviewedDigest string) (Workspace, error) {
	workspace, currentDigest, err := LoadWorkspaceWithDigest(root)
	if err != nil {
		return Workspace{}, err
	}
	if reviewedDigest == "" || currentDigest != reviewedDigest {
		return Workspace{}, errors.New("workspace manifest changed since review; preview the topology change again")
	}
	if workspace == nil {
		return Workspace{}, fmt.Errorf("%s has no workspace manifest", root)
	}
	if workspace.Mode != from {
		return Workspace{}, fmt.Errorf("workspace mode changed since review: expected %s, found %s", from, workspace.Mode)
	}
	if to != ModeUnified && to != ModeFederated {
		return Workspace{}, fmt.Errorf("mode must be %q or %q", ModeUnified, ModeFederated)
	}
	if from == to {
		return Workspace{}, fmt.Errorf("workspace is already %s", to)
	}
	workspace.Mode = to
	content, err := workspace.Encode()
	if err != nil {
		return Workspace{}, err
	}
	path := WorkspacePath(root)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workspace-*.toml")
	if err != nil {
		return Workspace{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return Workspace{}, err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return Workspace{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Workspace{}, err
	}
	if err := temporary.Close(); err != nil {
		return Workspace{}, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return Workspace{}, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return Workspace{}, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return Workspace{}, err
	}
	return *workspace, nil
}

// ErrWorkspaceRoot marks a Git operation attempted at a unified workspace folder,
// which groups repositories but is not one.
var ErrWorkspaceRoot = errors.New("Git-anchored records need a member repository; commits are not yet qualified by workspace member")

// RequireRepository returns ErrWorkspaceRoot when root is a workspace folder
// rather than a Git checkout. Other roots are left for Git itself to judge.
func RequireRepository(root string) error {
	if exists(filepath.Join(root, ".git")) {
		return nil
	}
	if workspace, err := LoadWorkspace(root); err == nil && workspace != nil {
		return fmt.Errorf("%s is a workspace folder, not a Git repository: %w", root, ErrWorkspaceRoot)
	}
	return nil
}
