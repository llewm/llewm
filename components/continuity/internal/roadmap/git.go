package roadmap

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

type Repository interface {
	Head(context.Context) (string, error)
	Dirty(context.Context) (bool, error)
	CountCommits(context.Context, string, string) (int, error)
	Files(context.Context, string) ([]string, error)
	ContentAtRevision(context.Context, string, string, int64) ([]byte, string, error)
}

func (repository GitRepository) ContentAtRevision(ctx context.Context, revision, path string, maxBytes int64) ([]byte, string, error) {
	if !exactCommitID.MatchString(revision) {
		return nil, "unsupported_reference", nil
	}
	objectType, err := repository.git(ctx, "cat-file", "-t", revision)
	if err != nil {
		return nil, "reference_unavailable", nil
	}
	if objectType != "commit" {
		return nil, "unsupported_reference", nil
	}
	spec := revision + ":" + path
	sizeOutput, err := repository.git(ctx, "cat-file", "-s", spec)
	if err != nil {
		return nil, "reference_unavailable", nil
	}
	size, err := strconv.ParseInt(sizeOutput, 10, 64)
	if err != nil {
		return nil, "reference_unavailable", nil
	}
	if size > maxBytes {
		return nil, "too_large", nil
	}
	content, err := repository.gitBytes(ctx, "cat-file", "blob", spec)
	if err != nil {
		return nil, "reference_unavailable", nil
	}
	return content, "available", nil
}

type GitRepository struct{ Root string }

var exactCommitID = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (repository GitRepository) git(ctx context.Context, args ...string) (string, error) {
	output, err := repository.gitBytes(ctx, args...)
	return strings.TrimSpace(string(output)), err
}

func (repository GitRepository) gitBytes(ctx context.Context, args ...string) ([]byte, error) {
	if err := project.RequireRepository(repository.Root); err != nil {
		return nil, err
	}
	command := gitenv.Command(ctx, append([]string{"-C", repository.Root}, args...)...)
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func (repository GitRepository) Head(ctx context.Context) (string, error) {
	return repository.git(ctx, "rev-parse", "HEAD")
}

func (repository GitRepository) Dirty(ctx context.Context) (bool, error) {
	output, err := repository.git(ctx, "status", "--porcelain=v1", "--untracked-files=normal")
	return output != "", err
}

func (repository GitRepository) CountCommits(ctx context.Context, from, to string) (int, error) {
	output, err := repository.git(ctx, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(output)
	if err != nil {
		return 0, fmt.Errorf("parse Git commit count %q: %w", output, err)
	}
	return count, nil
}

func (repository GitRepository) Files(ctx context.Context, path string) ([]string, error) {
	output, err := repository.gitBytes(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", path)
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return []string{}, nil
	}
	parts := strings.Split(string(output), "\x00")
	files := parts[:len(parts)-1]
	sort.Strings(files)
	return files, nil
}
