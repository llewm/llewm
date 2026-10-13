package roadmap

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/project"
)

// WorkspaceRepository reads a unified workspace's members as one roadmap
// repository. Its head is a composite "member@commit" list, so one sync
// watermark records every member; files and revisions route to the member whose
// path prefixes them.
type WorkspaceRepository struct {
	Repositories project.Repositories
}

// IsCompositeHead reports whether a sync watermark is a workspace composite.
func IsCompositeHead(value string) bool { return strings.Contains(value, "@") }

func parseComposite(value string) map[string]string {
	heads := map[string]string{}
	if !IsCompositeHead(value) {
		return heads
	}
	for _, part := range strings.Split(value, ",") {
		if member, commit, ok := strings.Cut(part, "@"); ok {
			heads[member] = commit
		}
	}
	return heads
}

func (w WorkspaceRepository) Head(ctx context.Context) (string, error) {
	var parts []string
	for _, repository := range w.Repositories.All() {
		head, err := (GitRepository{Root: repository.Root}).Head(ctx)
		if err != nil {
			return "", fmt.Errorf("workspace member %s: %w", repository.Member, err)
		}
		parts = append(parts, repository.Member+"@"+head)
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("workspace %s has no member checkouts", w.Repositories.Root)
	}
	return strings.Join(parts, ","), nil
}

func (w WorkspaceRepository) Dirty(ctx context.Context) (bool, error) {
	for _, repository := range w.Repositories.All() {
		dirty, err := (GitRepository{Root: repository.Root}).Dirty(ctx)
		if err != nil || dirty {
			return dirty, err
		}
	}
	return false, nil
}

// CountCommits sums commits per member since that member's watermark. A member
// the watermark does not name (added after the sync, or a watermark recorded
// before the project became a workspace) counts its whole history.
func (w WorkspaceRepository) CountCommits(ctx context.Context, from, to string) (int, error) {
	since, current := parseComposite(from), parseComposite(to)
	total := 0
	for _, repository := range w.Repositories.All() {
		head, ok := current[repository.Member]
		if !ok {
			continue
		}
		git := GitRepository{Root: repository.Root}
		var count int
		var err error
		if base, ok := since[repository.Member]; ok {
			count, err = git.CountCommits(ctx, base, head)
		} else {
			var output string
			if output, err = git.git(ctx, "rev-list", "--count", head); err == nil {
				count, err = strconv.Atoi(output)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("workspace member %s: %w", repository.Member, err)
		}
		total += count
	}
	return total, nil
}

// Files lists workspace-relative files under path, across every member it spans.
func (w WorkspaceRepository) Files(ctx context.Context, path string) ([]string, error) {
	path = filepath.ToSlash(filepath.Clean(path))
	var files []string
	if repository, relative, ok := w.Repositories.ForPath(path); ok {
		if relative == "" {
			relative = "."
		}
		listed, err := (GitRepository{Root: repository.Root}).Files(ctx, relative)
		if err != nil {
			return nil, err
		}
		for _, file := range listed {
			files = append(files, repository.Prefix()+file)
		}
		return files, nil
	}
	// A directory above the members covers each member inside it.
	for _, repository := range w.Repositories.All() {
		if path != "." && !strings.HasPrefix(repository.Prefix(), path+"/") {
			continue
		}
		listed, err := (GitRepository{Root: repository.Root}).Files(ctx, ".")
		if err != nil {
			return nil, err
		}
		for _, file := range listed {
			files = append(files, repository.Prefix()+file)
		}
	}
	sort.Strings(files)
	if files == nil {
		files = []string{}
	}
	return files, nil
}

// ContentAtRevision reads path at revision in the member owning path. The
// revision may be that member's commit or a composite watermark.
func (w WorkspaceRepository) ContentAtRevision(ctx context.Context, revision, path string, maxBytes int64) ([]byte, string, error) {
	repository, relative, ok := w.Repositories.ForPath(path)
	if !ok {
		return nil, "unsupported_reference", nil
	}
	if heads := parseComposite(revision); len(heads) > 0 {
		if revision, ok = heads[repository.Member]; !ok {
			return nil, "reference_unavailable", nil
		}
	}
	return (GitRepository{Root: repository.Root}).ContentAtRevision(ctx, revision, relative, maxBytes)
}
