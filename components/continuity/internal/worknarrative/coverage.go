package worknarrative

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

// Recent commits scanned for narrative coverage, and how many a line lists.
const (
	CoverageWindow     = 14 * 24 * time.Hour
	CoverageScan       = 20
	coverageListed     = 5
	coverageSubjectMax = 60
)

// UnnarratedCommit is a recent commit that no narrative describes.
type UnnarratedCommit struct {
	Commit      string    `json:"commit"`
	Member      string    `json:"member,omitempty"`
	Subject     string    `json:"subject"`
	CommittedAt time.Time `json:"committed_at"`
	// AuthorTask and AuthorSession name the task and session a hook saw create
	// the commit; empty when no attribution was recorded.
	AuthorTask    string `json:"author_task,omitempty"`
	AuthorSession string `json:"author_session,omitempty"`
}

// UnnarratedCommits lists the engineer's recent commits that no narrative
// describes, newest first. In each repository it scans the last CoverageScan
// non-merge commits on HEAD's first-parent history, keeps those committed within
// CoverageWindow and authored by the repository's user.email (every author when
// it is unset), and drops any that a work narrative anchors, current or
// superseded, or a commit narrative names. Git is only read.
func (s Service) UnnarratedCommits(ctx context.Context, projectID string) ([]UnnarratedCommit, error) {
	repositories := s.Repositories
	if repositories.Root == "" {
		repositories.Root = s.Root
	}
	cutoff := s.now().Add(-CoverageWindow)
	var result []UnnarratedCommit
	for _, repository := range repositories.All() {
		if _, err := s.git(ctx, repository.Root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
			continue // An unborn branch has nothing to cover.
		}
		email := ""
		if output, err := s.git(ctx, repository.Root, "config", "user.email"); err == nil {
			email = strings.TrimSpace(string(output))
		}
		output, err := s.git(ctx, repository.Root, "log", "--first-parent", "--no-merges", fmt.Sprintf("-n%d", CoverageScan), "--format=%H%x00%ae%x00%cI%x00%s", "HEAD")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			fields := strings.SplitN(line, "\x00", 4)
			if len(fields) != 4 {
				continue
			}
			committed, err := time.Parse(time.RFC3339, fields[2])
			if err != nil || committed.Before(cutoff) {
				continue
			}
			if email != "" && !strings.EqualFold(fields[1], email) {
				continue
			}
			var narrated bool
			if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_narrative_anchors WHERE project_id=? AND anchor_kind='commit' AND target=?)`, projectID, fields[0]).Scan(&narrated); err != nil {
				return nil, err
			}
			if !narrated {
				result = append(result, UnnarratedCommit{Commit: fields[0], Member: repository.Member, Subject: redact.String(fields[3]), CommittedAt: committed.UTC()})
			}
		}
	}
	return result, nil
}

// UnnarratedLine is a bounded, machine-observed advisory naming commits without
// a narrative, or "" when there are none.
func UnnarratedLine(commits []UnnarratedCommit) string {
	if len(commits) == 0 {
		return ""
	}
	listed := make([]string, 0, coverageListed)
	attributed := false
	for i, commit := range commits {
		if i == coverageListed {
			listed = append(listed, fmt.Sprintf("+%d more", len(commits)-coverageListed))
			break
		}
		name := commit.Commit[:min(len(commit.Commit), 7)]
		if commit.Member != "" {
			name = commit.Member + "@" + name
		}
		subject := commit.Subject
		if len(subject) > coverageSubjectMax {
			subject = truncateUTF8(subject, coverageSubjectMax) + "…"
		}
		entry := name + " " + subject
		switch {
		case commit.AuthorTask != "":
			entry += " (task " + commit.AuthorTask + ")"
			attributed = true
		case commit.AuthorSession != "":
			entry += " (session " + commit.AuthorSession + ")"
			attributed = true
		}
		listed = append(listed, entry)
	}
	noun := "commits have"
	if len(commits) == 1 {
		noun = "commit has"
	}
	line := fmt.Sprintf("Narrative coverage (machine-observed): %d recent %s no work narrative: %s. Record one per commit (narrative skill, or `llewm narrative record --commit HASH`).", len(commits), noun, strings.Join(listed, "; "))
	if attributed {
		line += " Narrate only commits your session or task made; a commit attributed to another task belongs to that task's agent."
	}
	return line
}
