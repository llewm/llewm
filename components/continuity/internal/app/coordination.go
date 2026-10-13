package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/authorship"
	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/project"
)

// Bounds keep coordination notices cheap to compute and small in the bootstrap.
const (
	footprintCheckpoints = 50
	footprintCommits     = 30
	changedCommitScan    = 50
	noticeFiles          = 4
	noticeItems          = 3
	overlapTasks         = 3
)

// taskFootprint is the set of repository paths a task is known to have
// touched: its checkpoints' changes, its narratives' file anchors, and the
// files of commits attributed to it.
func taskFootprint(ctx context.Context, db *sql.DB, repositories project.Repositories, projectID, taskID string, extra []string) map[string]bool {
	paths := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" && !strings.ContainsAny(value, " \t\n") && (strings.Contains(value, "/") || strings.Contains(value, ".")) {
			paths[path.Clean(strings.TrimPrefix(value, "./"))] = true
		}
	}
	for _, value := range extra {
		add(value)
	}
	if rows, err := db.QueryContext(ctx, `SELECT payload_json FROM checkpoints WHERE project_id=? AND task_id=? ORDER BY created_at DESC LIMIT ?`, projectID, taskID, footprintCheckpoints); err == nil {
		for rows.Next() {
			var payload string
			var value struct {
				Changes []string `json:"changes"`
			}
			if rows.Scan(&payload) == nil && json.Unmarshal([]byte(payload), &value) == nil {
				for _, change := range value.Changes {
					add(change)
				}
			}
		}
		rows.Close()
	}
	if rows, err := db.QueryContext(ctx, `SELECT DISTINCT f.target FROM work_narrative_anchors f JOIN work_narrative_anchors t ON t.project_id=f.project_id AND t.narrative_id=f.narrative_id
		WHERE f.project_id=? AND f.anchor_kind='file' AND t.anchor_kind='task' AND t.target=?`, projectID, taskID); err == nil {
		for rows.Next() {
			var target string
			if rows.Scan(&target) == nil {
				add(target)
			}
		}
		rows.Close()
	}
	commits, err := authorship.TaskCommits(ctx, db, projectID, taskID)
	if err != nil {
		return paths
	}
	roots := map[string]string{}
	for _, repository := range repositories.All() {
		roots[repository.Member] = repository.Root
	}
	scanned := 0
	for commit, member := range commits {
		root, ok := roots[member]
		if !ok || scanned == footprintCommits {
			continue
		}
		scanned++
		output, err := gitenv.Command(ctx, "-C", root, "show", "--name-only", "--format=", commit).Output()
		if err != nil {
			continue
		}
		for _, file := range strings.Fields(string(output)) {
			add(file)
		}
	}
	return paths
}

// touches reports whether a repository path is in a footprint. Footprint
// entries recorded relative to a subdirectory match as path suffixes.
func touches(footprint map[string]bool, file string) bool {
	if footprint[file] {
		return true
	}
	for entry := range footprint {
		if strings.HasSuffix(file, "/"+entry) || strings.HasSuffix(entry, "/"+file) {
			return true
		}
	}
	return false
}

// changedUnderneath names commits made since the task's latest checkpoint, by
// anyone other than the task, that changed files the task touched. It tells an
// agent resuming its task that another agent (or the engineer) edited its work.
func changedUnderneath(ctx context.Context, db *sql.DB, repositories project.Repositories, projectID string, latest *checkpoint.Checkpoint) string {
	if latest == nil || latest.TaskID == "" {
		return ""
	}
	recorded, err := checkpoint.GetContext(ctx, db, projectID, latest.ID)
	if err != nil || recorded == nil || len(recorded.CodeHeads) == 0 {
		return ""
	}
	footprint := taskFootprint(ctx, db, repositories, projectID, latest.TaskID, latest.Changes)
	if len(footprint) == 0 {
		return ""
	}
	bases := map[string]string{}
	for _, head := range recorded.CodeHeads {
		bases[head.Member] = head.Commit
	}
	var items []string
	total, other := 0, 0
	for _, repository := range repositories.All() {
		base, ok := bases[repository.Member]
		if !ok {
			continue
		}
		output, err := gitenv.Command(ctx, "-C", repository.Root, "log", "--max-count", fmt.Sprint(changedCommitScan), "--format=%x00%H%x09%s", "--name-only", base+"..HEAD").Output()
		if err != nil {
			continue
		}
		for _, block := range strings.Split(string(output), "\x00")[1:] {
			header, files, _ := strings.Cut(block, "\n")
			hash, subject, _ := strings.Cut(header, "\t")
			authors, _ := authorship.Authors(ctx, db, projectID, repository.Member, []string{hash})
			author, attributed := authors[hash]
			if attributed && author.TaskID == latest.TaskID {
				continue
			}
			var hit []string
			for _, file := range strings.Fields(files) {
				if touches(footprint, file) {
					hit = append(hit, file)
				}
			}
			if len(hit) == 0 {
				other++
				continue
			}
			total++
			if len(items) == noticeItems {
				continue
			}
			by := "not attributed to an agent"
			if attributed {
				by = "session " + author.SessionID
				if author.TaskID != "" {
					by = "task " + author.TaskID
				}
			}
			name := hash[:min(len(hash), 7)]
			if repository.Member != "" {
				name = repository.Member + "@" + name
			}
			items = append(items, fmt.Sprintf("%s %q (%s): %s", name, subject, by, listFiles(hit)))
		}
	}
	if total == 0 {
		return ""
	}
	line := fmt.Sprintf("Since this task's last checkpoint, %d commit(s) by others changed files it touched: %s", total, strings.Join(items, "; "))
	if total > len(items) {
		line += fmt.Sprintf("; +%d more", total-len(items))
	}
	if other > 0 {
		line += fmt.Sprintf(" (%d more commit(s) since then changed other files)", other)
	}
	return line + ". Review them before continuing; the checkpoint predates them."
}

// overlapNotice names files this task touched that recently active tasks also
// touched, so agents working in parallel notice shared files before editing.
func overlapNotice(ctx context.Context, db *sql.DB, repositories project.Repositories, projectID string, latest *checkpoint.Checkpoint, now time.Time) string {
	if latest == nil || latest.TaskID == "" {
		return ""
	}
	mine := taskFootprint(ctx, db, repositories, projectID, latest.TaskID, latest.Changes)
	if len(mine) == 0 {
		return ""
	}
	tips, err := checkpoint.TaskTips(ctx, db, projectID)
	if err != nil {
		return ""
	}
	var items []string
	checked := 0
	for _, tip := range tips {
		if tip.TaskID == latest.TaskID || tip.Status != "in_progress" || now.Sub(tip.CreatedAt) > bootstrap.ContentionWindow {
			continue
		}
		if closed, err := checkpoint.TaskClosed(ctx, db, projectID, tip.TaskID); err != nil || closed {
			continue
		}
		if checked == overlapTasks {
			break
		}
		checked++
		var shared []string
		for file := range taskFootprint(ctx, db, repositories, projectID, tip.TaskID, nil) {
			if touches(mine, file) {
				shared = append(shared, file)
			}
		}
		if len(shared) > 0 {
			sort.Strings(shared)
			items = append(items, fmt.Sprintf("`%s`: %s", tip.TaskID, listFiles(shared)))
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "Files this task touched are also in the work of other in-progress tasks: " + strings.Join(items, "; ") + ". Another agent may be editing them; coordinate before changing them."
}

func listFiles(files []string) string {
	sort.Strings(files)
	if len(files) <= noticeFiles {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:noticeFiles], ", ") + fmt.Sprintf(" +%d", len(files)-noticeFiles)
}
