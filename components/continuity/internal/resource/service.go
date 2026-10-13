// Package resource records the named things a project works with: hosts,
// repositories, and services. A resource describes how something is reached and
// set up, never a credential. Notes record dated observations of a resource's
// state. Resources are reference data: they grant no access and run nothing.
package resource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	SchemaVersion   = 1
	maxIDBytes      = 128
	maxTitleBytes   = 256
	maxSummaryBytes = 4 << 10
	maxNoteBytes    = 4 << 10
	maxValueBytes   = 1 << 10
	maxAttributes   = 32
	DefaultNotes    = 20
	MaxListLimit    = 100
	Disclaimer      = "Resources are caller-asserted reference data about things the project works with. They hold no credentials and grant no access or execution."
)

// Kinds are the supported resource kinds.
var Kinds = []string{"host", "repository", "service"}

var (
	attributeKey = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	// secretKey matches attribute names that would hold a credential itself.
	secretKey = regexp.MustCompile(`(?i)(pass(word|wd|phrase)|secret|token|api[_.-]?key|private[_.-]?key|privatekey)`)
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

type Resource struct {
	ProjectID     string            `json:"project_id"`
	ID            string            `json:"id"`
	Revision      int               `json:"revision"`
	Kind          string            `json:"kind"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Attributes    map[string]string `json:"attributes"`
	Status        string            `json:"status"`
	Creator       string            `json:"creator"`
	Model         string            `json:"model,omitempty"`
	Authority     string            `json:"authority"`
	RecordedAt    time.Time         `json:"recorded_at"`
	SchemaVersion int               `json:"schema_version"`
}

type Note struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ResourceID    string    `json:"resource_id"`
	Text          string    `json:"text"`
	Authority     string    `json:"authority"`
	Creator       string    `json:"creator"`
	ObservedAt    time.Time `json:"observed_at"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

type Detail struct {
	SchemaVersion  int      `json:"schema_version"`
	Resource       Resource `json:"resource"`
	Notes          []Note   `json:"notes"`
	NotesTruncated bool     `json:"notes_truncated"`
	Disclaimer     string   `json:"disclaimer"`
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

// NormalizeID accepts letters, digits, dot, underscore, and hyphen, and strips a
// resource: prefix.
func NormalizeID(value string) (string, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "resource:")
	if value == "" {
		return "", errors.New("resource ID is required")
	}
	if len(value) > maxIDBytes {
		return "", fmt.Errorf("resource ID exceeds %d bytes", maxIDBytes)
	}
	for _, r := range value {
		if r > 127 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return "", errors.New("resource ID must use letters, digits, dot, underscore, or hyphen")
		}
	}
	return value, nil
}

func bounded(name, value string, limit int, required bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > limit || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s must be at most %d bytes without NUL", name, limit)
	}
	return value, nil
}

// Validate returns value as Record would store it, or the reason it would be
// refused. It does not assign a revision or time.
func Validate(value Resource) (Resource, error) {
	var err error
	if value.ID, err = NormalizeID(value.ID); err != nil {
		return Resource{}, err
	}
	if !oneOf(value.Kind, Kinds...) {
		return Resource{}, fmt.Errorf("resource kind must be host, repository, or service, not %q", value.Kind)
	}
	if value.Title, err = bounded("title", value.Title, maxTitleBytes, true); err != nil {
		return Resource{}, err
	}
	if value.Summary, err = bounded("summary", value.Summary, maxSummaryBytes, false); err != nil {
		return Resource{}, err
	}
	if value.Creator, err = bounded("creator", value.Creator, maxIDBytes, true); err != nil {
		return Resource{}, err
	}
	if value.Model, err = bounded("model", value.Model, maxIDBytes, false); err != nil {
		return Resource{}, err
	}
	if len(value.Attributes) > maxAttributes {
		return Resource{}, fmt.Errorf("a resource may have at most %d attributes", maxAttributes)
	}
	attributes := make(map[string]string, len(value.Attributes))
	for key, raw := range value.Attributes {
		if !attributeKey.MatchString(key) {
			return Resource{}, fmt.Errorf("attribute name %q must be lowercase letters, digits, dot, underscore, or hyphen", key)
		}
		if secretKey.MatchString(key) {
			return Resource{}, fmt.Errorf("attribute %q names a credential; describe how authentication works instead, never the credential itself", key)
		}
		trimmed := strings.TrimSpace(raw)
		if redact.String(trimmed) != trimmed {
			return Resource{}, fmt.Errorf("attribute %q looks like it contains a credential; resources never store credentials", key)
		}
		if trimmed == "" || len(trimmed) > maxValueBytes || strings.IndexByte(trimmed, 0) >= 0 {
			return Resource{}, fmt.Errorf("attribute %q must be 1 to %d bytes without NUL", key, maxValueBytes)
		}
		attributes[key] = trimmed
	}
	value.Attributes = attributes
	if value.Status == "" {
		value.Status = "accepted"
		if value.Model != "" {
			value.Status = "proposed"
		}
	}
	if !oneOf(value.Status, "proposed", "accepted", "retired") {
		return Resource{}, fmt.Errorf("unsupported resource status %q", value.Status)
	}
	if value.Authority == "" {
		value.Authority = "caller_asserted"
		if value.Model != "" {
			value.Authority = "model_suggested"
		}
	}
	if !oneOf(value.Authority, "caller_asserted", "user_reported", "model_suggested") {
		return Resource{}, fmt.Errorf("unsupported authority %q", value.Authority)
	}
	if value.Model != "" && (value.Status != "proposed" || value.Authority != "model_suggested") {
		return Resource{}, errors.New("model-created resources must be proposed with model_suggested authority")
	}
	value.SchemaVersion = SchemaVersion
	return value, nil
}

// ValidateNote returns note as Note would store it, or the reason it would be
// refused. It does not assign an ID or time.
func ValidateNote(note Note) (Note, error) {
	var err error
	if note.ResourceID, err = NormalizeID(note.ResourceID); err != nil {
		return Note{}, err
	}
	if note.Text, err = bounded("note", note.Text, maxNoteBytes, true); err != nil {
		return Note{}, err
	}
	if note.Creator, err = bounded("creator", note.Creator, maxIDBytes, true); err != nil {
		return Note{}, err
	}
	if note.Authority == "" {
		note.Authority = "caller_asserted"
	}
	if !oneOf(note.Authority, "machine_observed", "user_reported", "caller_asserted") {
		return Note{}, fmt.Errorf("unsupported note authority %q", note.Authority)
	}
	note.SchemaVersion = SchemaVersion
	return note, nil
}

// Execer is a database or transaction that can insert rows.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Insert stores one already-validated resource revision as is.
func Insert(ctx context.Context, db Execer, value Resource) error {
	attributes, err := json.Marshal(value.Attributes)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO resources(project_id,id,revision,kind,title,summary,attributes_json,status,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`,
		value.ProjectID, value.ID, value.Revision, value.Kind, value.Title, value.Summary, string(attributes), value.Status, value.Creator, value.Model, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return err
}

// InsertNote stores one already-validated note as is.
func InsertNote(ctx context.Context, db Execer, note Note) error {
	_, err := db.ExecContext(ctx, `INSERT INTO resource_notes(id,project_id,resource_id,text,authority,creator,observed_at,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?)`,
		note.ID, note.ProjectID, note.ResourceID, note.Text, note.Authority, note.Creator, note.ObservedAt.Format(time.RFC3339Nano), note.RecordedAt.Format(time.RFC3339Nano), note.SchemaVersion)
	return err
}

// Record appends the next revision of a resource.
func (s Service) Record(ctx context.Context, value Resource) (Resource, error) {
	value, err := Validate(value)
	if err != nil {
		return Resource{}, err
	}
	value.RecordedAt = s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM resources WHERE project_id=? AND id=?`, value.ProjectID, value.ID).Scan(&value.Revision); err != nil {
		return Resource{}, err
	}
	if err = Insert(ctx, tx, value); err != nil {
		return Resource{}, err
	}
	return value, tx.Commit()
}

// AddNote appends a dated observation to an existing resource.
func (s Service) AddNote(ctx context.Context, note Note) (Note, error) {
	note, err := ValidateNote(note)
	if err != nil {
		return Note{}, err
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM resources WHERE project_id=? AND id=?`, note.ProjectID, note.ResourceID).Scan(&exists); err != nil {
		return Note{}, err
	}
	if exists == 0 {
		return Note{}, fmt.Errorf("resource %q was not found in this project", note.ResourceID)
	}
	note.ID, note.RecordedAt = activity.NewID(), s.now()
	if note.ObservedAt.IsZero() {
		note.ObservedAt = note.RecordedAt
	}
	note.ObservedAt = note.ObservedAt.UTC()
	return note, InsertNote(ctx, s.DB, note)
}

// Columns is the select list scanResource expects.
const Columns = `id,revision,kind,title,summary,attributes_json,status,creator,COALESCE(model,''),authority,recorded_at,schema_version`

// NoteColumns is the select list scanNote expects.
const NoteColumns = `id,resource_id,text,authority,creator,observed_at,recorded_at,schema_version`

type scanner interface{ Scan(...any) error }

func scanResource(row scanner, projectID string) (Resource, error) {
	value := Resource{ProjectID: projectID}
	var attributes, recorded string
	if err := row.Scan(&value.ID, &value.Revision, &value.Kind, &value.Title, &value.Summary, &attributes, &value.Status, &value.Creator, &value.Model, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
		return Resource{}, err
	}
	if err := json.Unmarshal([]byte(attributes), &value.Attributes); err != nil {
		return Resource{}, err
	}
	var err error
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return value, err
}

func scanNote(row scanner, projectID string) (Note, error) {
	note := Note{ProjectID: projectID}
	var observed, recorded string
	if err := row.Scan(&note.ID, &note.ResourceID, &note.Text, &note.Authority, &note.Creator, &observed, &recorded, &note.SchemaVersion); err != nil {
		return Note{}, err
	}
	var err error
	if note.ObservedAt, err = time.Parse(time.RFC3339Nano, observed); err != nil {
		return Note{}, err
	}
	note.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return note, err
}

// Queryer reads rows.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// All returns every resource revision and note of a project, for export.
func All(ctx context.Context, db Queryer, projectID string) ([]Resource, []Note, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+Columns+` FROM resources WHERE project_id=? ORDER BY id,revision`, projectID)
	if err != nil {
		return nil, nil, err
	}
	var resources []Resource
	for rows.Next() {
		value, err := scanResource(rows, projectID)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		resources = append(resources, value)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT `+NoteColumns+` FROM resource_notes WHERE project_id=? ORDER BY recorded_at,id`, projectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		note, err := scanNote(rows, projectID)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, note)
	}
	return resources, notes, rows.Err()
}

// Show returns one revision of a resource (the latest when revision is zero) and
// its most recent notes.
func (s Service) Show(ctx context.Context, projectID, id string, revision, noteLimit int) (Detail, error) {
	id, err := NormalizeID(id)
	if err != nil {
		return Detail{}, err
	}
	if noteLimit <= 0 {
		noteLimit = DefaultNotes
	}
	query := `SELECT ` + Columns + ` FROM resources WHERE project_id=? AND id=?`
	args := []any{projectID, id}
	if revision > 0 {
		query += ` AND revision=?`
		args = append(args, revision)
	}
	value, err := scanResource(s.DB.QueryRowContext(ctx, query+` ORDER BY revision DESC LIMIT 1`, args...), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, fmt.Errorf("resource %q was not found in this project", id)
	}
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{SchemaVersion: SchemaVersion, Resource: value, Notes: []Note{}, Disclaimer: Disclaimer}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+NoteColumns+` FROM resource_notes WHERE project_id=? AND resource_id=? ORDER BY observed_at DESC, id DESC LIMIT ?`, projectID, id, noteLimit+1)
	if err != nil {
		return Detail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		note, err := scanNote(rows, projectID)
		if err != nil {
			return Detail{}, err
		}
		detail.Notes = append(detail.Notes, note)
	}
	if len(detail.Notes) > noteLimit {
		detail.Notes, detail.NotesTruncated = detail.Notes[:noteLimit], true
	}
	return detail, rows.Err()
}

// List returns the latest revision of each resource, optionally filtered.
func (s Service) List(ctx context.Context, projectID, kind, status string, limit int) ([]Resource, error) {
	if kind != "" && !oneOf(kind, Kinds...) {
		return nil, fmt.Errorf("resource kind must be host, repository, or service, not %q", kind)
	}
	if status != "" && !oneOf(status, "proposed", "accepted", "retired") {
		return nil, fmt.Errorf("unsupported resource status %q", status)
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxListLimit)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+Columns+` FROM resources r WHERE project_id=? AND revision=(SELECT MAX(revision) FROM resources WHERE project_id=r.project_id AND id=r.id) AND (?='' OR kind=?) AND (?='' OR status=?) ORDER BY id LIMIT ?`, projectID, kind, kind, status, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Resource{}
	for rows.Next() {
		value, err := scanResource(rows, projectID)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
