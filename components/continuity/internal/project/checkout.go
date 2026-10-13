package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Checkout kinds reported by ResolveCheckout.
const (
	KindRepository = "repository"
	KindWorktree   = "worktree"
	KindSubmodule  = "submodule"
)

// StateDirName is the per-checkout Continuity state directory.
const StateDirName = ".continuity"

// Checkout identifies the Git working tree that contains a path and the project
// root whose store and identity apply to it.
type Checkout struct {
	// Root is the project root: the checkout itself, the main checkout of a
	// linked worktree, the superproject of a submodule, or a unified workspace.
	Root string `json:"root"`
	// Path is the Git working tree that contains the starting directory, or the
	// workspace folder itself.
	Path string `json:"path"`
	Kind string `json:"kind"`
	// Workspace is the unified workspace folder that claims this checkout, and
	// Member its manifest path; both are empty outside a workspace.
	Workspace string `json:"workspace,omitempty"`
	Member    string `json:"member,omitempty"`
}

// ResolveCheckout finds the Git working tree containing start and the project root
// it belongs to. Linked worktrees resolve to their main checkout and submodules to
// their superproject, unless that worktree or submodule has its own state
// directory, which makes it a standalone project.
func ResolveCheckout(start string) (Checkout, error) {
	path, err := FindRoot(start)
	if err != nil {
		return Checkout{}, err
	}
	kind, owner, err := classify(path)
	if err != nil {
		return Checkout{}, err
	}
	checkout := Checkout{Root: path, Path: path, Kind: kind}
	if kind == KindRepository || hasStateDir(path) {
		return checkout, nil
	}
	parent, err := ResolveCheckout(owner)
	if err != nil {
		return Checkout{}, err
	}
	checkout.Root = parent.Root
	return checkout, nil
}

// classify reports whether path is a plain repository, a linked worktree, or a
// submodule, and for the latter two the working tree that owns it.
func classify(path string) (string, string, error) {
	dotGit := filepath.Join(path, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return KindRepository, "", nil
	}
	gitDir, err := readGitFile(dotGit)
	if err != nil {
		return "", "", err
	}
	if common, ok := readCommonDir(gitDir); ok {
		// A linked worktree of a non-bare repository shares <main>/.git.
		if filepath.Base(common) == ".git" {
			return KindWorktree, filepath.Dir(common), nil
		}
		return KindRepository, "", nil
	}
	parentDir := filepath.Dir(path)
	if parentDir == path {
		return KindRepository, "", nil
	}
	super, err := FindRoot(parentDir)
	if err != nil {
		return KindRepository, "", nil
	}
	superGitDir, err := gitDirOf(super)
	if err != nil {
		return KindRepository, "", nil
	}
	if within(gitDir, filepath.Join(superGitDir, "modules")) {
		return KindSubmodule, super, nil
	}
	// A separate Git directory (git init --separate-git-dir) is still one repository.
	return KindRepository, "", nil
}

func readGitFile(dotGit string) (string, error) {
	content, err := os.ReadFile(dotGit)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(content), "\n", 2)[0])
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", errors.New("unrecognized .git file: " + dotGit)
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dotGit), target)
	}
	return filepath.Clean(target), nil
}

func readCommonDir(gitDir string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	common := strings.TrimSpace(string(content))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common), true
}

func gitDirOf(workingTree string) (string, error) {
	dotGit := filepath.Join(workingTree, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotGit, nil
	}
	return readGitFile(dotGit)
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasStateDir(path string) bool {
	info, err := os.Stat(filepath.Join(path, StateDirName))
	return err == nil && info.IsDir()
}

// StoreInside reports whether a database path lives inside root, meaning the store
// travels with the checkout when it moves.
func StoreInside(databasePath, root string) bool {
	return within(filepath.Clean(databasePath), filepath.Clean(root))
}
