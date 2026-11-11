package mcpserver

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

// maxSessionRecords bounds what one process remembers between checkpoints.
const maxSessionRecords = 64

// sessionLedger remembers the task this MCP process works on and the records
// it wrote since its last checkpoint. Clients start one stdio MCP process per
// session, so this attributes records to a session without a schema change or
// guessing from timestamps; parallel sessions have separate ledgers.
type sessionLedger struct {
	mu      sync.Mutex
	taskID  string
	records []string
}

// bind records the task this session last named through a task lease, a
// checkpoint, or a task-selected bootstrap.
func (l *sessionLedger) bind(taskID string) {
	if taskID = strings.TrimSpace(taskID); taskID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.taskID = taskID
}

func (l *sessionLedger) task() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.taskID
}

func (l *sessionLedger) add(ref string) {
	if ref == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, existing := range l.records {
		if existing == ref {
			return
		}
	}
	if len(l.records) < maxSessionRecords {
		l.records = append(l.records, ref)
	}
}

func (l *sessionLedger) written() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.records...)
}

// saved starts a new interval after a checkpoint names the task.
func (l *sessionLedger) saved(taskID string) {
	l.bind(taskID)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = nil
}

// anchorNarrative adds the bound task as an anchor when the caller named none,
// so findings written mid-task are found from the task without being asked.
func (l *sessionLedger) anchorNarrative(anchors []worknarrative.Anchor) []worknarrative.Anchor {
	taskID := l.task()
	if taskID == "" {
		return anchors
	}
	for _, anchor := range anchors {
		if anchor.Kind == "task" {
			return anchors
		}
	}
	return append(anchors, worknarrative.Anchor{Kind: "task", Target: taskID, Authority: "caller_asserted"})
}

// unlinkedRecords lists the refs this session wrote that neither link to the
// checkpoint's task nor appear in its evidence references.
func unlinkedRecords(ctx context.Context, db *sql.DB, projectID, taskID string, written, evidence []string) []string {
	listed := map[string]bool{}
	for _, ref := range evidence {
		listed[strings.TrimSpace(ref)] = true
	}
	var result []string
	for _, ref := range written {
		family, id, _ := strings.Cut(ref, ":")
		if listed[ref] || listed[id] {
			continue
		}
		if taskID != "" && linkedToTask(ctx, db, projectID, taskID, family, id) {
			continue
		}
		result = append(result, ref)
	}
	return result
}

func linkedToTask(ctx context.Context, db *sql.DB, projectID, taskID, family, id string) bool {
	var query string
	switch family {
	case "narrative":
		query = `SELECT EXISTS(SELECT 1 FROM work_narrative_anchors WHERE project_id=? AND narrative_id=? AND anchor_kind='task' AND target=?)`
	case "decision":
		query = `SELECT EXISTS(SELECT 1 FROM decision_links WHERE project_id=? AND decision_id=? AND target_kind='task' AND target_id=?)`
	case "work_item":
		query = `SELECT EXISTS(SELECT 1 FROM work_items WHERE project_id=? AND id=? AND task_id=?)`
	default:
		return false
	}
	var linked bool
	// A read failure reports the record as unlinked rather than hiding it.
	if err := db.QueryRowContext(ctx, query, projectID, id, taskID).Scan(&linked); err != nil {
		return false
	}
	return linked
}
