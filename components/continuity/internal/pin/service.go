// Package pin records which records the bootstrap should surface for a scope.
// A pin is an append-only pin or unpin assertion about a target record and a
// scope (the project, a topic, a roadmap node, or a task); the latest assertion
// for a target and scope wins. Pins never change the pinned record and grant no
// authority.
package pin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	SchemaVersion = 1
	maxIDBytes    = 256
	Disclaimer    = "Pins are caller-asserted reminders of relevant records. They do not change the pinned records or grant instruction or execution authority."
)

var (
	TargetKinds = []string{"resource", "procedure", "decision", "topic"}
	ScopeKinds  = []string{"project", "topic", "roadmap_node", "task"}
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

type Pin struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	TargetKind    string    `json:"target_kind"`
	TargetID      string    `json:"target_id"`
	ScopeKind     string    `json:"scope_kind"`
	ScopeID       string    `json:"scope_id"`
	Action        string    `json:"action"`
	Creator       string    `json:"creator"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// Line is one current pin as the bootstrap shows it.
type Line struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	Scope string `json:"scope"`
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// ParseRef splits "kind:id" into its parts.
func ParseRef(ref string) (string, string, error) {
	kind, id, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || id == "" {
		return "", "", fmt.Errorf("reference %q must look like kind:id", ref)
	}
	return kind, id, nil
}

// Validate returns value as Set would store it, or the reason it would be refused.
func Validate(value Pin) (Pin, error) {
	if !oneOf(value.TargetKind, TargetKinds...) {
		return Pin{}, fmt.Errorf("pin target must be a resource, procedure, decision, or topic, not %q", value.TargetKind)
	}
	if !oneOf(value.ScopeKind, ScopeKinds...) {
		return Pin{}, fmt.Errorf("pin scope must be project, topic, roadmap_node, or task, not %q", value.ScopeKind)
	}
	value.TargetID, value.ScopeID = strings.TrimSpace(value.TargetID), strings.TrimSpace(value.ScopeID)
	if value.TargetID == "" || len(value.TargetID) > maxIDBytes || redact.String(value.TargetID) != value.TargetID {
		return Pin{}, errors.New("pin target ID is required and must be a plain identifier")
	}
	if (value.ScopeKind == "project") != (value.ScopeID == "") {
		return Pin{}, errors.New("a project pin has no scope ID; other scopes need one")
	}
	if len(value.ScopeID) > maxIDBytes || redact.String(value.ScopeID) != value.ScopeID {
		return Pin{}, errors.New("pin scope ID must be a plain identifier")
	}
	if value.Action == "" {
		value.Action = "pin"
	}
	if !oneOf(value.Action, "pin", "unpin") {
		return Pin{}, fmt.Errorf("pin action must be pin or unpin, not %q", value.Action)
	}
	value.Creator = strings.TrimSpace(redact.String(value.Creator))
	if value.Creator == "" || len(value.Creator) > maxIDBytes {
		return Pin{}, errors.New("pin creator is required")
	}
	if value.Authority == "" {
		value.Authority = "caller_asserted"
	}
	if !oneOf(value.Authority, "caller_asserted", "user_reported") {
		return Pin{}, fmt.Errorf("unsupported pin authority %q", value.Authority)
	}
	value.SchemaVersion = SchemaVersion
	return value, nil
}

// Execer is a database or transaction that can insert rows.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Insert stores one already-validated pin assertion as is.
func Insert(ctx context.Context, db Execer, value Pin) error {
	_, err := db.ExecContext(ctx, `INSERT INTO pins(id,project_id,target_kind,target_id,scope_kind,scope_id,action,creator,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		value.ID, value.ProjectID, value.TargetKind, value.TargetID, value.ScopeKind, value.ScopeID, value.Action, value.Creator, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

var targetExists = map[string]string{
	"resource":  `SELECT COUNT(*) FROM resources WHERE project_id=? AND id=?`,
	"procedure": `SELECT COUNT(*) FROM reviewed_procedures WHERE project_id=? AND procedure_id=?`,
	"decision":  `SELECT COUNT(*) FROM decision_records WHERE project_id=? AND id=?`,
	"topic":     `SELECT COUNT(*) FROM topic_revisions WHERE project_id=? AND id=?`,
}

// Set appends a pin or unpin assertion. The target must exist in this project.
func (s Service) Set(ctx context.Context, value Pin) (Pin, error) {
	value, err := Validate(value)
	if err != nil {
		return Pin{}, err
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, targetExists[value.TargetKind], value.ProjectID, value.TargetID).Scan(&count); err != nil {
		return Pin{}, err
	}
	if count == 0 {
		return Pin{}, fmt.Errorf("%s:%s was not found in this project", value.TargetKind, value.TargetID)
	}
	value.ID, value.RecordedAt = activity.NewID(), s.now()
	return value, Insert(ctx, s.DB, value)
}

// Columns is the select list scanPin expects.
const Columns = `id,target_kind,target_id,scope_kind,scope_id,action,creator,authority,recorded_at,schema_version`

func scanPin(row interface{ Scan(...any) error }, projectID string) (Pin, error) {
	value := Pin{ProjectID: projectID}
	var recorded string
	if err := row.Scan(&value.ID, &value.TargetKind, &value.TargetID, &value.ScopeKind, &value.ScopeID, &value.Action, &value.Creator, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
		return Pin{}, err
	}
	var err error
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return value, err
}

// Queryer reads rows.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// All returns every pin assertion of a project, for export.
func All(ctx context.Context, db Queryer, projectID string) ([]Pin, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+Columns+` FROM pins WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Pin
	for rows.Next() {
		value, err := scanPin(rows, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

// currentPinned selects targets whose latest assertion for a scope is a pin.
const currentPinned = `SELECT p.target_kind,p.target_id,p.scope_kind,p.scope_id FROM pins p
WHERE p.project_id=? AND p.action='pin' AND NOT EXISTS (
  SELECT 1 FROM pins q WHERE q.project_id=p.project_id AND q.target_kind=p.target_kind AND q.target_id=p.target_id
  AND q.scope_kind=p.scope_kind AND q.scope_id=p.scope_id AND (q.recorded_at>p.recorded_at OR (q.recorded_at=p.recorded_at AND q.id>p.id)))`

var titleQueries = map[string]string{
	"resource":  `SELECT title FROM resources WHERE project_id=? AND id=? ORDER BY revision DESC LIMIT 1`,
	"procedure": `SELECT title FROM reviewed_procedures WHERE project_id=? AND procedure_id=? ORDER BY revision DESC LIMIT 1`,
	"decision":  `SELECT title FROM decision_records WHERE project_id=? AND id=?`,
	"topic":     `SELECT title FROM topic_revisions WHERE project_id=? AND id=? ORDER BY revision DESC LIMIT 1`,
}

// Current returns every current pin in the project, with target titles, most
// general scope first. A target that has not arrived yet (for example, pinned on
// another machine before its record synced) shows its reference as the title.
func Current(ctx context.Context, db Queryer, projectID string) ([]Line, error) {
	rows, err := db.QueryContext(ctx, currentPinned+` ORDER BY CASE p.scope_kind WHEN 'project' THEN 0 WHEN 'roadmap_node' THEN 1 WHEN 'topic' THEN 2 ELSE 3 END, p.scope_id, p.target_kind, p.target_id`, projectID)
	if err != nil {
		return nil, err
	}
	type pinned struct{ targetKind, targetID, scopeKind, scopeID string }
	var values []pinned
	for rows.Next() {
		var value pinned
		if err := rows.Scan(&value.targetKind, &value.targetID, &value.scopeKind, &value.scopeID); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	lines := make([]Line, 0, len(values))
	for _, value := range values {
		ref := value.targetKind + ":" + value.targetID
		title := ref
		if err := db.QueryRowContext(ctx, titleQueries[value.targetKind], projectID, value.targetID).Scan(&title); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		scope := value.scopeKind
		if value.scopeID != "" {
			scope += ":" + value.scopeID
		}
		lines = append(lines, Line{Ref: ref, Title: title, Scope: scope})
	}
	return lines, nil
}

// ForTask returns the current pins whose scope is the project, the task, a
// roadmap node the task belongs to (an active binding or a track plan), or a
// topic with an accepted link to the task or one of those nodes.
func ForTask(ctx context.Context, db Queryer, projectID, taskID string) ([]Line, error) {
	all, err := Current(ctx, db, projectID)
	if err != nil || len(all) == 0 {
		return all, err
	}
	scopes := map[string]bool{"project": true}
	if taskID != "" {
		scopes["task:"+taskID] = true
		nodes, err := TaskRoadmapNodes(ctx, db, projectID, taskID)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			scopes["roadmap_node:"+node] = true
		}
		query := `SELECT DISTINCT topic_id FROM topic_links WHERE project_id=? AND status='accepted' AND ((target_kind='task' AND target_id=?)`
		args := []any{projectID, taskID}
		if len(nodes) > 0 {
			query += ` OR (target_kind='roadmap_node' AND target_id IN (?` + strings.Repeat(",?", len(nodes)-1) + `))`
			for _, node := range nodes {
				args = append(args, node)
			}
		}
		topics, err := queryStrings(ctx, db, query+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, topic := range topics {
			scopes["topic:"+topic] = true
		}
	}
	lines := make([]Line, 0, len(all))
	for _, line := range all {
		if scopes[line.Scope] {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// TaskRoadmapNodes returns the roadmap nodes a task belongs to. Task bindings
// stay on the machine that made them, but track plans sync, so both count.
func TaskRoadmapNodes(ctx context.Context, db Queryer, projectID, taskID string) ([]string, error) {
	return queryStrings(ctx, db, `SELECT b.roadmap_node_id FROM roadmap_task_bindings b WHERE b.project_id=? AND b.task_id=? AND b.status='active' AND b.revision=(SELECT MAX(revision) FROM roadmap_task_bindings WHERE project_id=b.project_id AND task_id=b.task_id AND roadmap_node_id=b.roadmap_node_id)
UNION SELECT roadmap_node_id FROM track_plan_revisions WHERE project_id=? AND task_id=?`, projectID, taskID, projectID, taskID)
}

func queryStrings(ctx context.Context, db Queryer, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
