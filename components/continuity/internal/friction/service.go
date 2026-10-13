package friction

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	maxFieldBytes     = 16 << 10
	maxSignatureBytes = 512
	maxDimensions     = 32
	maxRetryLookahead = 5
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

type toolEvent struct {
	id, sessionID, taskID, client, outcome string
	at                                     time.Time
	tool, command, errorSignature          string
}

type candidateBuilder struct {
	candidate Candidate
	clients   map[string]struct{}
	tasks     map[string]struct{}
	refs      []string
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Service) Report(ctx context.Context, projectID string, options Options) (Report, error) {
	options, err := validateOptions(options)
	if err != nil {
		return Report{}, err
	}
	candidates, scanned, total, err := s.derive(ctx, projectID, options.ObservationLimit)
	if err != nil {
		return Report{}, err
	}
	tracked, err := s.trackedItems(ctx, projectID)
	if err != nil {
		return Report{}, err
	}
	filtered := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.OccurrenceCount < options.MinCount {
			continue
		}
		candidate.TrackedBy = tracked[candidate.ID]
		filtered = append(filtered, candidate)
	}
	report := Report{
		SchemaVersion: 1, Candidates: []Candidate{}, CandidateCount: len(filtered), EventsScanned: scanned,
		EventsTotal: total, ScanTruncated: total > scanned, MaxBytes: options.MaxBytes,
		Authority:  "machine_observed",
		Disclaimer: "Deterministic friction candidates are observations for review; they do not establish cause, change configuration, or authorize a remedy.",
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# Repeated friction candidates\n\n- Tool events scanned: %d of %d\n- Minimum occurrences: %d\n\n", scanned, total, options.MinCount)
	body.WriteString("Derived grouping does not establish cause or authorize a remedy.\n\n")
	limited := filtered
	if len(limited) > options.Limit {
		limited = limited[:options.Limit]
		report.Truncated = true
	}
	for _, candidate := range limited {
		section := renderCandidate(candidate)
		if body.Len()+len(section) > options.MaxBytes {
			report.Truncated = true
			break
		}
		body.WriteString(section)
		report.Candidates = append(report.Candidates, candidate)
	}
	if len(report.Candidates) < len(filtered) {
		report.Truncated = true
	}
	if report.Truncated {
		marker := "_Additional candidates omitted by report bounds._\n"
		if body.Len()+len(marker) <= options.MaxBytes {
			body.WriteString(marker)
		}
	}
	report.IncludedCount = len(report.Candidates)
	report.Markdown = body.String()
	report.RenderedBytes = len([]byte(report.Markdown))
	return report, nil
}

func validateOptions(options Options) (Options, error) {
	if options.MinCount == 0 {
		options.MinCount = 2
	}
	if options.Limit == 0 {
		options.Limit = DefaultLimit
	}
	if options.ObservationLimit == 0 {
		options.ObservationLimit = DefaultObservationLimit
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MinCount < 1 || options.MinCount > 1000 {
		return Options{}, fmt.Errorf("minimum count must be between 1 and 1000")
	}
	if options.Limit < 1 || options.Limit > 100 {
		return Options{}, fmt.Errorf("limit must be between 1 and 100")
	}
	if options.ObservationLimit < 1 || options.ObservationLimit > 20 {
		return Options{}, fmt.Errorf("observation limit must be between 1 and 20")
	}
	if options.MaxBytes < 512 || options.MaxBytes > MaxRenderBytes {
		return Options{}, fmt.Errorf("max bytes must be between 512 and %d", MaxRenderBytes)
	}
	return options, nil
}

func (s Service) derive(ctx context.Context, projectID string, observationLimit int) ([]Candidate, int, int, error) {
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE project_id=? AND category='tool'`, projectID).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,occurred_at,COALESCE(session_id,''),COALESCE(task_id,''),client,COALESCE(outcome,''),payload_json FROM (SELECT id,occurred_at,session_id,task_id,client,outcome,payload_json FROM events WHERE project_id=? AND category='tool' ORDER BY occurred_at DESC,id DESC LIMIT ?) ORDER BY occurred_at,id`, projectID, MaxScannedEvents)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	events := []toolEvent{}
	for rows.Next() {
		var event toolEvent
		var at, payloadJSON string
		if err = rows.Scan(&event.id, &at, &event.sessionID, &event.taskID, &event.client, &event.outcome, &payloadJSON); err != nil {
			return nil, 0, 0, err
		}
		event.at, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, 0, 0, err
		}
		var payload map[string]any
		if err = json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, 0, 0, err
		}
		event.tool, _ = payload["tool"].(string)
		event.command, _ = payload["command"].(string)
		event.errorSignature, _ = payload["error_signature"].(string)
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	groups := map[string]*candidateBuilder{}
	for i, failure := range events {
		if failure.outcome != "failure" || failure.sessionID == "" {
			continue
		}
		family := commandFamily(failure.tool, failure.command)
		if family == "" {
			continue
		}
		recovery, ok := nextRecovery(events, i, failure.client, failure.sessionID, family)
		if !ok {
			continue
		}
		signature := normalizeSignature(failure.errorSignature)
		if signature == "" {
			continue
		}
		recoveryShape := commandShape(recovery.tool, recovery.command)
		key := family + "\n" + signature + "\n" + recoveryShape
		sum := sha256.Sum256([]byte(key))
		id := hex.EncodeToString(sum[:16])
		group := groups[id]
		if group == nil {
			group = &candidateBuilder{candidate: Candidate{ID: id, CommandFamily: family, FailureSignature: signature, RecoveryShape: recoveryShape, FirstSeenAt: failure.at, LastSeenAt: failure.at, Cause: "unknown"}, clients: map[string]struct{}{}, tasks: map[string]struct{}{}}
			groups[id] = group
		}
		group.candidate.OccurrenceCount++
		if failure.at.Before(group.candidate.FirstSeenAt) {
			group.candidate.FirstSeenAt = failure.at
		}
		if failure.at.After(group.candidate.LastSeenAt) {
			group.candidate.LastSeenAt = failure.at
		}
		group.clients[failure.client] = struct{}{}
		if failure.taskID != "" {
			group.tasks[failure.taskID] = struct{}{}
		}
		for _, id := range []string{failure.id, recovery.id} {
			ref := "event:" + id
			if len(group.refs) < observationLimit && !contains(group.refs, ref) {
				group.refs = append(group.refs, ref)
			}
		}
	}
	out := make([]Candidate, 0, len(groups))
	for _, group := range groups {
		var clientsTruncated, tasksTruncated bool
		group.candidate.Clients, clientsTruncated = boundedSet(group.clients, maxDimensions)
		group.candidate.TaskIDs, tasksTruncated = boundedSet(group.tasks, maxDimensions)
		if clientsTruncated || tasksTruncated {
			group.candidate.DimensionsTruncated = true
		}
		group.candidate.ObservationRefs = group.refs
		out = append(out, group.candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurrenceCount != out[j].OccurrenceCount {
			return out[i].OccurrenceCount > out[j].OccurrenceCount
		}
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, len(events), total, nil
}

func nextRecovery(events []toolEvent, index int, client, sessionID, family string) (toolEvent, bool) {
	seen := 0
	for i := index + 1; i < len(events) && seen < maxRetryLookahead; i++ {
		if events[i].client != client || events[i].sessionID != sessionID {
			continue
		}
		seen++
		if events[i].outcome != "failure" && commandFamily(events[i].tool, events[i].command) == family {
			return events[i], true
		}
	}
	return toolEvent{}, false
}

func commandFamily(tool, command string) string {
	tool = redact.String(tool)
	command = redact.String(command)
	fields := strings.Fields(command)
	for len(fields) > 0 && (fields[0] == "env" || isAssignment(fields[0])) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return strings.ToLower(strings.TrimSpace(tool))
	}
	fields[0] = filepath.Base(fields[0])
	count := 1
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "-") {
		count = 2
	}
	return strings.ToLower(strings.Join(fields[:count], " "))
}

func commandShape(tool, command string) string {
	tool = redact.String(tool)
	command = redact.String(command)
	fields := strings.Fields(command)
	environment := []string{}
	for len(fields) > 0 {
		if fields[0] == "env" {
			fields = fields[1:]
			continue
		}
		name, ok := assignmentName(fields[0])
		if !ok {
			break
		}
		environment = append(environment, name+"=*")
		fields = fields[1:]
	}
	family := commandFamily(tool, strings.Join(fields, " "))
	return strings.TrimSpace(strings.Join(append(environment, family), " "))
}

func isAssignment(value string) bool {
	_, ok := assignmentName(value)
	return ok
}

func assignmentName(value string) (string, bool) {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return "", false
	}
	for i, r := range name {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return "", false
		}
	}
	return name, true
}

func normalizeSignature(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(redact.String(value)), " "))
	return trimUTF8(value, maxSignatureBytes)
}

func trimUTF8(value string, max int) string {
	if len([]byte(value)) <= max {
		return value
	}
	body := []byte(value)[:max]
	for !utf8.Valid(body) {
		body = body[:len(body)-1]
	}
	return string(body)
}

func boundedSet(values map[string]struct{}, limit int) ([]string, bool) {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > limit {
		return out[:limit], true
	}
	return out, false
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func renderCandidate(candidate Candidate) string {
	clients := strings.Join(candidate.Clients, ", ")
	if clients == "" {
		clients = "none recorded"
	}
	tasks := strings.Join(candidate.TaskIDs, ", ")
	if tasks == "" {
		tasks = "none recorded"
	}
	refs := strings.Join(candidate.ObservationRefs, ", ")
	var body strings.Builder
	fmt.Fprintf(&body, "## `%s`\n\n- Command family: `%s`\n- Failure signature: %s\n- Recovery shape: `%s`\n- Occurrences: %d\n- Time range: %s to %s\n- Clients: %s\n- Tasks: %s\n- Representative observations: %s\n- Cause: unknown\n", "friction:"+candidate.ID, candidate.CommandFamily, candidate.FailureSignature, candidate.RecoveryShape, candidate.OccurrenceCount, candidate.FirstSeenAt.Format(time.RFC3339Nano), candidate.LastSeenAt.Format(time.RFC3339Nano), clients, tasks, refs)
	if len(candidate.TrackedBy) == 0 {
		fmt.Fprintf(&body, "- Tracked by: none; to track it, capture a bug with from `friction:%s`\n", candidate.ID)
	}
	for _, item := range candidate.TrackedBy {
		fmt.Fprintf(&body, "- Tracked by: `%s` (%s, %s)\n", item.Ref, item.Kind, item.Status)
	}
	body.WriteString("\n")
	return body.String()
}

// trackedItems maps candidate IDs to the work items linked derived_from them.
func (s Service) trackedItems(ctx context.Context, projectID string) (map[string][]TrackedItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT substr(l.target_ref,10),w.id,w.kind,COALESCE((SELECT d.status FROM work_item_dispositions d WHERE d.project_id=w.project_id AND d.item_id=w.id ORDER BY d.recorded_at DESC,d.id DESC LIMIT 1),'pending')
		FROM work_item_links l JOIN work_items w ON w.project_id=l.project_id AND w.id=l.item_id
		WHERE l.project_id=? AND l.relationship='derived_from' AND l.target_ref LIKE 'friction:%' ORDER BY w.recorded_at,w.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]TrackedItem{}
	for rows.Next() {
		var candidate, id string
		var item TrackedItem
		if err := rows.Scan(&candidate, &id, &item.Kind, &item.Status); err != nil {
			return nil, err
		}
		item.Ref = "work_item:" + id
		out[candidate] = append(out[candidate], item)
	}
	return out, rows.Err()
}
