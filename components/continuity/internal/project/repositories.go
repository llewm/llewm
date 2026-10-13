package project

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// Repository is one Git checkout a project reads commits from. Member is the
// workspace member path, or empty for a single-repository project.
type Repository struct {
	Member string `json:"member,omitempty"`
	Root   string `json:"root"`
}

// Prefix is the workspace-relative path prefix of the repository's files.
func (r Repository) Prefix() string {
	if r.Member == "" {
		return ""
	}
	return filepath.ToSlash(r.Member) + "/"
}

// Repositories selects the Git checkout for commit-anchored operations. A
// single-repository project has exactly its root. In a unified workspace, each
// present member is a repository, and Default is the member containing the
// working directory.
type Repositories struct {
	Root      string
	Workspace *Workspace
	Default   string
}

// Workspaced reports whether commits must be qualified by member.
func (r Repositories) Workspaced() bool {
	return r.Workspace != nil && r.Workspace.Mode == ModeUnified
}

// Select returns the named member, the default member, or the only member.
func (r Repositories) Select(member string) (Repository, error) {
	member = strings.Trim(filepath.ToSlash(strings.TrimSpace(member)), "/")
	if !r.Workspaced() {
		if member != "" {
			return Repository{}, fmt.Errorf("--repo %q applies only in a unified workspace", member)
		}
		return Repository{Root: r.Root}, nil
	}
	if member == "" {
		member = r.Default
	}
	if member == "" && len(r.Workspace.Members) == 1 {
		member = r.Workspace.Members[0].Path
	}
	names := make([]string, 0, len(r.Workspace.Members))
	for _, candidate := range r.Workspace.Members {
		path := filepath.ToSlash(filepath.Clean(candidate.Path))
		if path == member {
			return Repository{Member: path, Root: r.Workspace.MemberRoot(candidate)}, nil
		}
		names = append(names, path)
	}
	sort.Strings(names)
	if member == "" {
		return Repository{}, fmt.Errorf("at the workspace folder, name the member repository (--repo, or repo over MCP): %s", strings.Join(names, ", "))
	}
	return Repository{}, fmt.Errorf("%q is not a member of workspace %s; members: %s", member, r.Workspace.Name, strings.Join(names, ", "))
}

// All returns every repository whose checkout is present, ordered by member.
func (r Repositories) All() []Repository {
	if !r.Workspaced() {
		return []Repository{{Root: r.Root}}
	}
	var result []Repository
	for _, member := range r.Workspace.Members {
		root := r.Workspace.MemberRoot(member)
		if exists(filepath.Join(root, ".git")) {
			result = append(result, Repository{Member: filepath.ToSlash(filepath.Clean(member.Path)), Root: root})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Member < result[j].Member })
	return result
}

// CheckoutRoots returns every present working tree for the project's
// repositories. Git worktrees may live outside a unified workspace or the main
// checkout, so consumers that attribute activity by cwd must not use Root alone.
// A repository whose worktree listing fails still contributes its primary root.
func (r Repositories) CheckoutRoots(ctx context.Context) []string {
	roots := map[string]bool{}
	for _, repository := range r.All() {
		root := filepath.Clean(repository.Root)
		roots[root] = true
		output, err := gitenv.Command(ctx, "-C", root, "worktree", "list", "--porcelain", "-z").Output()
		if err != nil {
			continue
		}
		for _, field := range bytes.Split(output, []byte{0}) {
			path, ok := bytes.CutPrefix(field, []byte("worktree "))
			if !ok || len(path) == 0 {
				continue
			}
			candidate := filepath.Clean(string(path))
			if exists(filepath.Join(candidate, ".git")) {
				roots[candidate] = true
			}
		}
	}
	result := make([]string, 0, len(roots))
	for root := range roots {
		result = append(result, root)
	}
	sort.Strings(result)
	return result
}

// ForPath returns the repository owning a project-relative path and the path
// relative to that repository. In a workspace, the owner is the member whose path
// prefixes it; paths outside every member have no repository.
func (r Repositories) ForPath(path string) (Repository, string, bool) {
	path = filepath.ToSlash(filepath.Clean(path))
	if !r.Workspaced() {
		return Repository{Root: r.Root}, path, true
	}
	var best Repository
	for _, repository := range r.All() {
		if (path == repository.Member || strings.HasPrefix(path, repository.Prefix())) && len(repository.Member) > len(best.Member) {
			best = repository
		}
	}
	if best.Member == "" {
		return Repository{}, "", false
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(path, best.Member), "/"), true
}
