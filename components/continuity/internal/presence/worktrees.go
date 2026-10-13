package presence

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

type Worktree struct {
	Member       string   `json:"member,omitempty"`
	Path         string   `json:"path"`
	Branch       string   `json:"branch,omitempty"`
	Head         string   `json:"head,omitempty"`
	Clean        bool     `json:"clean"`
	Integrated   bool     `json:"integrated"`
	LiveSessions []string `json:"live_sessions"`
	CleanupDue   bool     `json:"cleanup_due"`
	Reason       string   `json:"reason"`
}

// CleanupCandidates inspects linked worktrees without changing them. A
// worktree is cleanup-due only when it is clean, its HEAD is contained in the
// repository's primary HEAD, and no live presence claim names its exact path.
func CleanupCandidates(ctx context.Context, repositories project.Repositories, current []Event, now time.Time) []Worktree {
	var result []Worktree
	for _, repository := range repositories.All() {
		for _, value := range linkedWorktrees(ctx, repository) {
			if filepath.Clean(value.Path) == filepath.Clean(repository.Root) {
				continue
			}
			value.LiveSessions = liveAt(current, value.Path, now)
			status, err := gitenv.Command(ctx, "-C", value.Path, "status", "--porcelain").Output()
			value.Clean = err == nil && len(bytes.TrimSpace(status)) == 0
			value.Integrated = value.Head != "" && gitenv.Command(ctx, "-C", repository.Root, "merge-base", "--is-ancestor", value.Head, "HEAD").Run() == nil
			switch {
			case len(value.LiveSessions) > 0:
				value.Reason = "live presence claims this worktree"
			case !value.Clean:
				value.Reason = "worktree has uncommitted changes"
			case !value.Integrated:
				value.Reason = "worktree HEAD is not contained in the primary checkout HEAD"
			default:
				value.CleanupDue = true
				value.Reason = "clean, integrated, and inactive"
			}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Member != result[j].Member {
			return result[i].Member < result[j].Member
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func linkedWorktrees(ctx context.Context, repository project.Repository) []Worktree {
	output, err := gitenv.Command(ctx, "-C", repository.Root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var values []Worktree
	var current *Worktree
	flush := func() {
		if current != nil && current.Path != "" {
			values = append(values, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(string(output), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = &Worktree{Member: repository.Member, Path: filepath.Clean(strings.TrimPrefix(line, "worktree "))}
		case current != nil && strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case current != nil && strings.HasPrefix(line, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "":
			flush()
		}
	}
	flush()
	return values
}

func liveAt(values []Event, path string, now time.Time) []string {
	var sessions []string
	path = filepath.Clean(path)
	for _, value := range values {
		if value.Live(now) && value.WorktreePath != "" && filepath.Clean(value.WorktreePath) == path {
			sessions = append(sessions, value.SessionID)
		}
	}
	sort.Strings(sessions)
	return sessions
}
