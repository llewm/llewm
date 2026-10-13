package brief

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

// TaskBrief expands one exact task tip without repeating global/project guidance.
// All checkpoint text remains a saved claim, not a live source verification.
type TaskBrief struct {
	SchemaVersion int                  `json:"schema_version"`
	TaskID        string               `json:"task_id"`
	CheckpointRef string               `json:"checkpoint_ref"`
	RecordedAt    string               `json:"recorded_at"`
	Task          string               `json:"task"`
	Status        string               `json:"status"`
	Summary       string               `json:"summary"`
	NextAction    string               `json:"next_action"`
	Verified      []string             `json:"verification_claims,omitempty"`
	Attempts      []checkpoint.Attempt `json:"attempts,omitempty"`
	Decisions     []DecisionRef        `json:"decisions,omitempty"`
	Items         []Brief              `json:"named_items,omitempty"`
	EvidenceRefs  []string             `json:"evidence_refs,omitempty"`
	Omitted       map[string]int       `json:"omitted,omitempty"`
	Disclaimer    string               `json:"disclaimer"`
}

func BuildTask(ctx context.Context, db *sql.DB, projectID, taskID string) (TaskBrief, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskBrief{}, fmt.Errorf("task ID is required")
	}
	cp, err := (checkpoint.Service{DB: db}).LatestSelected(ctx, projectID, checkpoint.Selection{TaskID: taskID})
	if err != nil {
		return TaskBrief{}, err
	}
	if cp == nil {
		return TaskBrief{}, fmt.Errorf("no checkpoint for task %q", taskID)
	}
	b := TaskBrief{SchemaVersion: 1, TaskID: cp.TaskID, CheckpointRef: "checkpoint:" + cp.ID, RecordedAt: cp.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), Task: cp.Task, Status: cp.Status, Summary: cp.Summary, NextAction: cp.NextAction, Verified: cp.Verified, Attempts: cp.Attempts, EvidenceRefs: cp.EvidenceRefs, Omitted: map[string]int{}, Disclaimer: "Saved checkpoint and decision claims, not live verification or execution authority. Exact task routing; no prose-derived links. Guidance is not repeated. Expand checkpoint_ref for omitted detail and verify changed sources before reusing checks."}
	// Current direct task decisions only. Superseded decisions remain accessible by ref.
	rows, err := db.QueryContext(ctx, `SELECT d.id,d.title,d.disposition FROM decision_records d WHERE d.project_id=? AND EXISTS(SELECT 1 FROM decision_links l WHERE l.project_id=d.project_id AND l.decision_id=d.id AND l.target_kind='task' AND l.target_id=?) AND NOT EXISTS(SELECT 1 FROM decision_records s WHERE s.project_id=d.project_id AND s.supersedes_id=d.id) ORDER BY d.recorded_at DESC,d.id LIMIT 17`, projectID, taskID)
	if err != nil {
		return TaskBrief{}, err
	}
	for rows.Next() {
		var d DecisionRef
		if err := rows.Scan(&d.ID, &d.Title, &d.Disposition); err != nil {
			rows.Close()
			return TaskBrief{}, err
		}
		b.Decisions = append(b.Decisions, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return TaskBrief{}, err
	}
	if len(b.Decisions) > 16 {
		b.Decisions = b.Decisions[:16]
		b.Omitted["decisions_at_least"] = 1
	}
	items, err := Mentioned(ctx, db, projectID, cp.NextAction)
	if err != nil {
		return TaskBrief{}, err
	}
	for _, c := range items {
		value, err := Build(ctx, db, projectID, c.Ref())
		if err != nil {
			return TaskBrief{}, err
		}
		if value.TaskID != taskID {
			continue
		}
		if len(b.Items) == 3 {
			b.Omitted["named_items"]++
			continue
		}
		b.Items = append(b.Items, value)
	}
	return boundTask(b)
}

func boundTask(b TaskBrief) (TaskBrief, error) {
	// Deduplicate exact checkpoint references; no similarity or semantic inference.
	seen := map[string]bool{}
	refs := []string{}
	for _, ref := range b.EvidenceRefs {
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	b.EvidenceRefs = refs
	for {
		body, _ := json.MarshalIndent(b, "", "  ")
		if len(body) <= MaxBytes && len(TaskMarkdown(b)) <= MaxBytes {
			return b, nil
		}
		switch {
		case len(b.EvidenceRefs) > 0:
			b.EvidenceRefs = b.EvidenceRefs[:len(b.EvidenceRefs)-1]
			b.Omitted["evidence_refs"]++
		case len(b.Items) > 0:
			b.Items = b.Items[:len(b.Items)-1]
			b.Omitted["named_items"]++
		case len(b.Verified) > 0:
			b.Verified = b.Verified[:len(b.Verified)-1]
			b.Omitted["verification_claims"]++
		case len(b.Attempts) > 0:
			b.Attempts = b.Attempts[:len(b.Attempts)-1]
			b.Omitted["attempts"]++
		case len(b.Decisions) > 0:
			b.Decisions = b.Decisions[:len(b.Decisions)-1]
			b.Omitted["decisions"]++
		default:
			// A saved kernel itself can exceed the read budget. Report byte omissions
			// explicitly and preserve its exact checkpoint reference for expansion.
			changed := false
			for name, value := range map[string]*string{"summary": &b.Summary, "next_action": &b.NextAction, "task": &b.Task} {
				if len(*value) > 128 {
					n := len(*value)
					*value = clip(*value, min(512, n/2))
					b.Omitted[name+"_bytes"] += n - len(*value)
					changed = true
				}
			}
			if !changed {
				return TaskBrief{}, fmt.Errorf("task brief identity exceeds %d byte budget; expand checkpoint directly", MaxBytes)
			}
		}
	}
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func TaskMarkdown(b TaskBrief) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Task: %s [%s]\n\n%s\n\nTip: %s · %s\n\n%s\n\nNext: %s\n", b.TaskID, b.Status, b.Task, b.CheckpointRef, b.RecordedAt, b.Summary, b.NextAction)
	if len(b.Verified) > 0 {
		out.WriteString("\n## Saved verification claims\n")
		for _, s := range b.Verified {
			fmt.Fprintf(&out, "- %s\n", s)
		}
	}
	if len(b.Attempts) > 0 {
		out.WriteString("\n## Attempts\n")
		for _, a := range b.Attempts {
			fmt.Fprintf(&out, "- %s — %s", a.Description, a.Outcome)
			if a.Reason != "" {
				fmt.Fprintf(&out, " (%s)", a.Reason)
			}
			out.WriteString("\n")
		}
	}
	if len(b.Decisions) > 0 {
		out.WriteString("\n## Current task decisions\n")
		for _, d := range b.Decisions {
			fmt.Fprintf(&out, "- decision:%s [%s]: %s\n", d.ID, d.Disposition, d.Title)
		}
	}
	for _, item := range b.Items {
		out.WriteString("\n" + Markdown(item))
	}
	if len(b.EvidenceRefs) > 0 {
		fmt.Fprintf(&out, "\nEvidence: %s\n", strings.Join(b.EvidenceRefs, ", "))
	}
	if len(b.Omitted) > 0 {
		keys := []string{}
		for k := range b.Omitted {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteString("\nOmitted:")
		for _, key := range keys {
			fmt.Fprintf(&out, " %s=%d", key, b.Omitted[key])
		}
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, "\n%s\n", b.Disclaimer)
	return out.String()
}
