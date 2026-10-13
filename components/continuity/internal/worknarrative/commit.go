package worknarrative

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// CommitAnchors describes what AddCommit derived from Git for one commit.
type CommitAnchors struct {
	Commit       string    `json:"commit"`
	CommittedAt  time.Time `json:"committed_at"`
	ChangedPaths int       `json:"changed_paths"`
	FileAnchors  int       `json:"file_anchors"`
	Deleted      int       `json:"deleted_paths"`
	Omitted      int       `json:"omitted_paths"`
}

// MemberLocator names the workspace member holding a commit anchor's commit; it
// is empty outside a workspace.
func MemberLocator(member string) string {
	if member == "" {
		return ""
	}
	return "member:" + member
}

func (s Service) git(ctx context.Context, root string, args ...string) ([]byte, error) {
	if err := project.RequireRepository(root); err != nil {
		return nil, err
	}
	command := gitenv.Command(ctx, append([]string{"-C", root}, args...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func (s Service) repository(member string) (project.Repository, error) {
	repositories := s.Repositories
	if repositories.Root == "" {
		repositories.Root = s.Root
	}
	return repositories.Select(member)
}

// AddCommit resolves ref in the project repository and adds machine-observed
// anchors to the record: the exact commit, then each path the commit changed
// (first-parent diff), hashed from its content at that commit, until the anchor
// limit is reached. Deleted paths have no content and are counted only. When the
// record has no occurred time, the commit time is used. Git is only read.
func (s Service) AddCommit(ctx context.Context, record Record, ref string) (Record, CommitAnchors, error) {
	return s.AddCommitIn(ctx, record, ref, "")
}

// AddCommitIn is AddCommit in a workspace member ("" selects the default member).
// File anchors are workspace-relative, and the commit anchor's region locator
// names the member.
func (s Service) AddCommitIn(ctx context.Context, record Record, ref, member string) (Record, CommitAnchors, error) {
	var result CommitAnchors
	repository, err := s.repository(member)
	if err != nil {
		return record, result, err
	}
	root := repository.Root
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") {
		return record, result, fmt.Errorf("commit reference is required and cannot start with -")
	}
	output, err := s.git(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return record, result, err
	}
	result.Commit = strings.TrimSpace(string(output))
	if output, err = s.git(ctx, root, "show", "-s", "--format=%cI", result.Commit, "--"); err != nil {
		return record, result, err
	}
	if result.CommittedAt, err = time.Parse(time.RFC3339, strings.TrimSpace(string(output))); err != nil {
		return record, result, err
	}
	result.CommittedAt = result.CommittedAt.UTC()
	if output, err = s.git(ctx, root, "diff-tree", "--no-commit-id", "-r", "--root", "-m", "--first-parent", "--no-renames", "-z", "--name-status", result.Commit); err != nil {
		return record, result, err
	}
	var paths []string
	seen := map[string]bool{}
	parts := strings.Split(strings.TrimRight(string(output), "\x00"), "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		status, path := parts[i], parts[i+1]
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		result.ChangedPaths++
		if strings.HasPrefix(status, "D") {
			result.Deleted++
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	locator := MemberLocator(repository.Member)
	for _, anchor := range record.Anchors {
		if anchor.Kind == "commit" && anchor.Target == result.Commit && anchor.RegionLocator == locator {
			return record, result, fmt.Errorf("record already anchors commit %s", result.Commit)
		}
	}
	record.Anchors = append([]Anchor{{Kind: "commit", Target: result.Commit, RegionLocator: locator, Authority: "machine_observed"}}, record.Anchors...)
	for _, path := range paths {
		if len(record.Anchors) >= MaxAnchors {
			result.Omitted++
			continue
		}
		content, err := s.git(ctx, root, "cat-file", "blob", result.Commit+":"+path)
		if err != nil {
			// Submodules and other non-blob entries have no content to hash.
			result.Omitted++
			continue
		}
		sum := sha256.Sum256(content)
		record.Anchors = append(record.Anchors, Anchor{Kind: "file", Target: repository.Prefix() + path, ContentSHA256: hex.EncodeToString(sum[:]), Authority: "machine_observed"})
		result.FileAnchors++
	}
	if record.OccurredFrom == nil && record.OccurredTo == nil {
		when := result.CommittedAt
		record.OccurredFrom, record.OccurredTo = &when, &when
	}
	if record.Trigger == "" {
		record.Trigger = "commit"
	}
	return record, result, nil
}
