// Package researchcapture derives a cheap, advisory comparison between
// discovery activity and the durable records produced by that work.
package researchcapture

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const MinDiscoveryOperations = 6

type Candidate struct {
	SessionID               string
	TaskID                  string
	PredecessorCheckpointID string
	EvidenceRefs            int
	Completed               int
	Verified                int
	Attempts                int
}

type Report struct {
	CitedClaims         int      `json:"cited_claims"`
	UncitedFindings     int      `json:"uncited_findings"`
	SourceObservations  int      `json:"source_observations"`
	State               string   `json:"state"`
	DiscoveryOperations int      `json:"discovery_operations"`
	DiscoveryKinds      []string `json:"discovery_kinds"`
	DurableRecords      int      `json:"durable_records"`
	Narratives          int      `json:"narratives"`
	Decisions           int      `json:"decisions"`
	WorkItems           int      `json:"work_items"`
	EvidenceRefs        int      `json:"evidence_refs"`
	CheckpointSignals   int      `json:"checkpoint_signals"`
	Since               string   `json:"since,omitempty"`
	Reasons             []string `json:"reasons,omitempty"`
	Authority           string   `json:"authority"`
	Disclaimer          string   `json:"disclaimer"`
}

type Service struct{ DB *sql.DB }

// ReportForCheckpoint returns nil when the checkpoint has no session identity
// or the bounded activity since its predecessor is not research-heavy. It reads
// only already-redacted tool names and commands; it never loads tool results.
func (s Service) ReportForCheckpoint(ctx context.Context, projectID string, candidate Candidate) (*Report, error) {
	if candidate.SessionID == "" {
		return nil, nil
	}
	since, err := s.since(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	operations, kinds, err := s.discovery(ctx, projectID, candidate.SessionID, since)
	if err != nil {
		return nil, err
	}
	if operations < MinDiscoveryOperations || len(kinds) < 2 && operations < 10 {
		return nil, nil
	}
	report := &Report{
		State: "review_recommended", DiscoveryOperations: operations, DiscoveryKinds: kinds,
		EvidenceRefs: candidate.EvidenceRefs, CheckpointSignals: checkpointSignals(candidate), Since: since,
		Authority:  "machine_observed",
		Disclaimer: "This advisory compares bounded activity counts with attributable durable records. It does not inspect hidden reasoning, store tool results, judge semantic quality, or require more records.",
	}
	if candidate.TaskID != "" {
		if report.Narratives, err = count(ctx, s.DB, `SELECT COUNT(DISTINCT n.id) FROM work_narratives n JOIN work_narrative_anchors a ON a.project_id=n.project_id AND a.narrative_id=n.id WHERE n.project_id=? AND a.anchor_kind='task' AND a.target=? AND (?='' OR n.recorded_at>?)`, projectID, candidate.TaskID, since, since); err != nil {
			return nil, err
		}
		if report.Decisions, err = count(ctx, s.DB, `SELECT COUNT(DISTINCT d.id) FROM decision_records d JOIN decision_links l ON l.project_id=d.project_id AND l.decision_id=d.id WHERE d.project_id=? AND l.target_kind='task' AND l.target_id=? AND (?='' OR d.recorded_at>?)`, projectID, candidate.TaskID, since, since); err != nil {
			return nil, err
		}
		if report.WorkItems, err = count(ctx, s.DB, `SELECT COUNT(*) FROM work_items WHERE project_id=? AND task_id=? AND (?='' OR recorded_at>?)`, projectID, candidate.TaskID, since, since); err != nil {
			return nil, err
		}
	}
	report.DurableRecords = report.Narratives + report.Decisions + report.WorkItems
	if contains(kinds, "web") {
		base := ` FROM work_narratives n JOIN work_narrative_claims c ON c.project_id=n.project_id AND c.narrative_id=n.id WHERE n.project_id=? AND EXISTS(SELECT 1 FROM work_narrative_anchors a WHERE a.project_id=n.project_id AND a.narrative_id=n.id AND a.anchor_kind='task' AND a.target=?) AND (?='' OR n.recorded_at>?) AND NOT EXISTS(SELECT 1 FROM superseded_records s WHERE s.project_id=n.project_id AND s.family='narrative' AND s.id=n.id)`
		args := []any{projectID, candidate.TaskID, since, since}
		if report.CitedClaims, err = count(ctx, s.DB, `SELECT COUNT(*)`+base+` AND EXISTS(SELECT 1 FROM work_narrative_citations e WHERE e.project_id=n.project_id AND e.narrative_id=n.id AND e.claim_ordinal=c.ordinal)`, args...); err != nil {
			return nil, err
		}
		if report.UncitedFindings, err = count(ctx, s.DB, `SELECT COUNT(*)`+base+` AND json_type(n.payload_json,'$.source') IS NULL AND c.kind IN ('outcome','verification') AND NOT EXISTS(SELECT 1 FROM work_narrative_citations e WHERE e.project_id=n.project_id AND e.narrative_id=n.id AND e.claim_ordinal=c.ordinal)`, args...); err != nil {
			return nil, err
		}
		if report.SourceObservations, err = count(ctx, s.DB, `SELECT COUNT(DISTINCT e.source_id) FROM work_narrative_citations e WHERE e.project_id=? AND e.narrative_id IN (SELECT n.id`+base+`)`, append([]any{projectID}, args...)...); err != nil {
			return nil, err
		}
		if report.CitedClaims == 0 || report.SourceObservations == 0 || report.UncitedFindings > 0 {
			report.Reasons = []string{"web discovery needs task-linked findings with structured source citations; unrelated records and checkpoint references do not establish source coverage"}
			if report.UncitedFindings > 0 {
				report.Reasons = append(report.Reasons, fmt.Sprintf("%d outcome/verification claims lack citations; review which actually depend on web evidence", report.UncitedFindings))
			}
			return report, nil
		}
	}
	if report.DurableRecords > 0 || report.EvidenceRefs > 0 || report.CheckpointSignals >= 2 {
		report.State = "covered"
		return report, nil
	}
	report.Reasons = []string{
		"substantial discovery activity produced no task-linked narrative, decision, or work item in this interval",
		"the checkpoint names no evidence references and has little structured completion, verification, or attempt detail",
	}
	return report, nil
}

func (s Service) since(ctx context.Context, projectID string, candidate Candidate) (string, error) {
	var since string
	if candidate.PredecessorCheckpointID != "" {
		err := s.DB.QueryRowContext(ctx, `SELECT created_at FROM checkpoints WHERE project_id=? AND id=?`, projectID, candidate.PredecessorCheckpointID).Scan(&since)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if since != "" {
			return since, nil
		}
	}
	err := s.DB.QueryRowContext(ctx, `SELECT started_at FROM sessions WHERE project_id=? AND id=?`, projectID, candidate.SessionID).Scan(&since)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	return since, nil
}

func (s Service) discovery(ctx context.Context, projectID, sessionID, since string) (int, []string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT COALESCE(payload_json,'{}') FROM events WHERE project_id=? AND session_id=? AND (?='' OR occurred_at>?) ORDER BY occurred_at,id`, projectID, sessionID, since, since)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	operations := 0
	kinds := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, nil, err
		}
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		tool, _ := payload["tool"].(string)
		command, _ := payload["command"].(string)
		if kind := discoveryKind(tool, command); kind != "" {
			operations++
			kinds[kind] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	result := make([]string, 0, len(kinds))
	for kind := range kinds {
		result = append(result, kind)
	}
	sort.Strings(result)
	return operations, result, nil
}

func discoveryKind(tool, command string) string {
	name := strings.ToLower(strings.TrimSpace(tool))
	switch {
	case strings.Contains(name, "continuity_history_search"), strings.Contains(name, "continuity_history_show"):
		return "history"
	case name == "read", name == "grep", name == "glob", strings.HasSuffix(name, "__read"), strings.HasSuffix(name, "__grep"), strings.HasSuffix(name, "__glob"):
		return "repository"
	case strings.Contains(name, "web__run"), strings.Contains(name, "websearch"), strings.Contains(name, "webfetch"), strings.Contains(name, "search_service"), strings.Contains(name, "web_search"), strings.Contains(name, "web_fetch"):
		return "web"
	}
	if name != "bash" && !strings.Contains(name, "exec") {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(command))
	for _, marker := range []string{"llewm history search", "llewm history show"} {
		if strings.Contains(value, marker) {
			return "history"
		}
	}
	for _, marker := range []string{"rg ", "git log", "git show", "git blame", "sed -n", "go doc", "find "} {
		if strings.HasPrefix(value, marker) || strings.Contains(value, " "+marker) {
			return "repository"
		}
	}
	return ""
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func checkpointSignals(candidate Candidate) int {
	result := 0
	for _, count := range []int{candidate.Completed, candidate.Verified, candidate.Attempts} {
		if count > 0 {
			result++
		}
	}
	return result
}

func count(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var value int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, fmt.Errorf("research capture count: %w", err)
	}
	return value, nil
}

func Line(report *Report) string {
	if report == nil {
		return ""
	}
	line := fmt.Sprintf("Research capture: %s; discovery=%d across %s; durable=%d (narratives=%d decisions=%d work_items=%d), evidence_refs=%d, checkpoint_signals=%d.", report.State, report.DiscoveryOperations, strings.Join(report.DiscoveryKinds, ","), report.DurableRecords, report.Narratives, report.Decisions, report.WorkItems, report.EvidenceRefs, report.CheckpointSignals)
	if len(report.Reasons) > 0 {
		line += " " + strings.Join(report.Reasons, "; ") + "."
	}
	return line
}
