// Package authorship records which agent session a hook saw create a commit,
// so narrative coverage and overlap notices can tell an agent's own commits
// from another agent's. Attribution is machine-observed and advisory.
package authorship

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/gitenv"
)

// MaxCommitsPerObservation bounds how many new commits one hook attributes.
const MaxCommitsPerObservation = 50

var fullHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// ReadHead resolves a checkout's HEAD commit from its Git files, without a
// subprocess, so hooks can check it on every event. It follows a symbolic
// HEAD through loose and packed refs, including worktrees' common directory,
// and reports false when it cannot resolve HEAD that way.
func ReadHead(root string) (string, bool) {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", false
	}
	if !info.IsDir() {
		body, err := os.ReadFile(gitDir)
		if err != nil {
			return "", false
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:")
		if !ok {
			return "", false
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	common := gitDir
	if body, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = strings.TrimSpace(string(body))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	body, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(string(body))
	ref, symbolic := strings.CutPrefix(head, "ref: ")
	if !symbolic {
		return head, fullHash.MatchString(head)
	}
	for _, dir := range []string{gitDir, common} {
		if value, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			hash := strings.TrimSpace(string(value))
			return hash, fullHash.MatchString(hash)
		}
	}
	packed, err := os.Open(filepath.Join(common, "packed-refs"))
	if err != nil {
		return "", false
	}
	defer packed.Close()
	scanner := bufio.NewScanner(packed)
	for scanner.Scan() {
		hash, name, ok := strings.Cut(scanner.Text(), " ")
		if ok && name == ref && fullHash.MatchString(hash) {
			return hash, true
		}
	}
	return "", false
}

// Head resolves HEAD from files, falling back to git when that fails.
func Head(ctx context.Context, root string) (string, error) {
	if hash, ok := ReadHead(root); ok {
		return hash, nil
	}
	output, err := gitenv.Command(ctx, "-C", root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}").Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(output)), nil
}

var gitWrite = regexp.MustCompile(`\bgit\b[^\n]*\b(commit|merge|rebase|cherry-pick|revert|pull|am)\b`)

// RanGitWrite reports whether a tool call ran a Git command that can create
// commits, judged from its client-reported input.
func RanGitWrite(toolInput any) bool {
	switch value := toolInput.(type) {
	case nil:
		return false
	case string:
		return gitWrite.MatchString(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, part := range value {
			if text, ok := part.(string); ok {
				parts = append(parts, text)
			}
		}
		return gitWrite.MatchString(strings.Join(parts, " "))
	case map[string]any:
		for _, field := range value {
			if RanGitWrite(field) {
				return true
			}
		}
	}
	return false
}

// Observation is one hook's view of a checkout.
type Observation struct {
	ProjectID, Member, Root string
	SessionID, Client       string
	TaskID, ToolName        string
	// Attribute is true when this hook's tool call ran a Git command that can
	// create commits; only then are new commits attributed to the session.
	Attribute bool
	At        time.Time
}

// Observe compares HEAD with the last HEAD any hook saw for this checkout. When
// it moved during a tool call that ran Git, the new commits are attributed to
// the observing session; otherwise the baseline just moves, so commits made
// outside any agent's tool call are never attributed to one.
func Observe(ctx context.Context, db *sql.DB, observation Observation) (int, error) {
	head, err := Head(ctx, observation.Root)
	if err != nil || head == "" {
		return 0, err
	}
	var previous string
	err = db.QueryRowContext(ctx, `SELECT head FROM head_observations WHERE project_id=? AND member=?`, observation.ProjectID, observation.Member).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if previous == head {
		return 0, nil
	}
	at := observation.At.UTC().Format(time.RFC3339Nano)
	recorded := 0
	if previous != "" && observation.Attribute && observation.SessionID != "" {
		commits := []string{head}
		output, err := gitenv.Command(ctx, "-C", observation.Root, "rev-list", "--max-count", strconv.Itoa(MaxCommitsPerObservation), previous+".."+head).Output()
		if err == nil {
			commits = strings.Fields(string(output))
		}
		for _, commit := range commits {
			result, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO commit_authors(project_id,member,commit_sha,session_id,client,task_id,tool_name,certainty,observed_at) VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),'observed',?)`,
				observation.ProjectID, observation.Member, commit, observation.SessionID, observation.Client, observation.TaskID, observation.ToolName, at)
			if err != nil {
				return recorded, err
			}
			if changed, _ := result.RowsAffected(); changed == 1 {
				recorded++
			}
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO head_observations(project_id,member,head,observed_at) VALUES (?,?,?,?)
		ON CONFLICT(project_id,member) DO UPDATE SET head=excluded.head, observed_at=excluded.observed_at`, observation.ProjectID, observation.Member, head, at)
	return recorded, err
}

// Author is the session (and its task) a hook saw create a commit.
type Author struct {
	SessionID string `json:"session_id"`
	Client    string `json:"client"`
	TaskID    string `json:"task_id,omitempty"`
}

// Authors returns the recorded authors of commits in one checkout, by hash.
func Authors(ctx context.Context, db *sql.DB, projectID, member string, commits []string) (map[string]Author, error) {
	result := map[string]Author{}
	for _, commit := range commits {
		var author Author
		err := db.QueryRowContext(ctx, `SELECT session_id,client,COALESCE(task_id,'') FROM commit_authors WHERE project_id=? AND member=? AND commit_sha=?`, projectID, member, commit).Scan(&author.SessionID, &author.Client, &author.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[commit] = author
	}
	return result, nil
}

// TaskCommits lists the commits attributed to a task's sessions in a checkout.
func TaskCommits(ctx context.Context, db *sql.DB, projectID, taskID string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT commit_sha,member FROM commit_authors WHERE project_id=? AND task_id=?`, projectID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var commit, member string
		if err := rows.Scan(&commit, &member); err != nil {
			return nil, err
		}
		result[commit] = member
	}
	return result, rows.Err()
}
