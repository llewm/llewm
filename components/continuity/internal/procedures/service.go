package procedures

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	maxTextBytes = 16 << 10
	maxItems     = 64
	maxSteps     = 32
)

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func clean(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maxTextBytes {
		return "", fmt.Errorf("%s exceeds 16 KiB limit", name)
	}
	return value, nil
}

func normalizeStrings(name string, values []string, requireOne bool) ([]string, error) {
	if requireOne && len(values) == 0 {
		return nil, fmt.Errorf("%s must contain at least one item", name)
	}
	if len(values) > maxItems {
		return nil, fmt.Errorf("%s may contain at most %d items", name, maxItems)
	}
	result := make([]string, len(values))
	seen := map[string]bool{}
	for i, value := range values {
		cleaned, err := clean(name, value, true)
		if err != nil {
			return nil, err
		}
		if seen[cleaned] {
			return nil, fmt.Errorf("%s contains duplicate %q", name, cleaned)
		}
		seen[cleaned], result[i] = true, cleaned
	}
	return result, nil
}

func normalizeSequence(name string, values []string) ([]string, error) {
	if len(values) > maxItems {
		return nil, fmt.Errorf("%s may contain at most %d items", name, maxItems)
	}
	result := make([]string, len(values))
	for i, value := range values {
		cleaned, err := clean(name, value, true)
		if err != nil {
			return nil, err
		}
		result[i] = cleaned
	}
	return result, nil
}

func normalizeProcedure(value Procedure) (Procedure, error) {
	var err error
	if value.ProjectID, err = clean("project ID", value.ProjectID, true); err != nil {
		return Procedure{}, err
	}
	if value.ID, err = clean("procedure ID", value.ID, true); err != nil {
		return Procedure{}, err
	}
	if value.Title, err = clean("title", value.Title, true); err != nil {
		return Procedure{}, err
	}
	if value.Purpose, err = clean("purpose", value.Purpose, true); err != nil {
		return Procedure{}, err
	}
	if value.Kind, err = NormalizeKind(value.Kind); err != nil {
		return Procedure{}, err
	}
	if value.Status != "proposed" && value.Status != "accepted" && value.Status != "superseded" {
		return Procedure{}, fmt.Errorf("unsupported procedure status %q", value.Status)
	}
	if value.Source, err = clean("source", value.Source, true); err != nil {
		return Procedure{}, err
	}
	if value.SourceRef, err = clean("source reference", value.SourceRef, false); err != nil {
		return Procedure{}, err
	}
	if value.Platforms, err = normalizeStrings("platforms", value.Platforms, true); err != nil {
		return Procedure{}, err
	}
	if value.WorkingDirectory == "" {
		value.WorkingDirectory = "."
	}
	if value.WorkingDirectory, err = clean("working directory", value.WorkingDirectory, true); err != nil {
		return Procedure{}, err
	}
	if filepath.IsAbs(value.WorkingDirectory) || value.WorkingDirectory != filepath.Clean(value.WorkingDirectory) || value.WorkingDirectory == ".." || strings.HasPrefix(value.WorkingDirectory, ".."+string(filepath.Separator)) {
		return Procedure{}, fmt.Errorf("working directory must be a clean project-relative path")
	}
	if value.DeclaredEffects, err = normalizeStrings("declared effects", value.DeclaredEffects, value.Kind == KindRunbook); err != nil {
		return Procedure{}, err
	}
	if value.Validations, err = normalizeStrings("validations", value.Validations, false); err != nil {
		return Procedure{}, err
	}
	if value.PostRunChecks, err = normalizeStrings("post-run checks", value.PostRunChecks, false); err != nil {
		return Procedure{}, err
	}
	if len(value.Parameters) > maxItems {
		return Procedure{}, fmt.Errorf("parameters may contain at most %d items", maxItems)
	}
	parameterNames := map[string]bool{}
	for i := range value.Parameters {
		if value.Parameters[i].Name, err = clean("parameter name", value.Parameters[i].Name, true); err != nil {
			return Procedure{}, err
		}
		if value.Parameters[i].Description, err = clean("parameter description", value.Parameters[i].Description, true); err != nil {
			return Procedure{}, err
		}
		switch value.Parameters[i].ResourceKind {
		case "", "host", "repository", "service":
		default:
			return Procedure{}, fmt.Errorf("parameter %q resource kind must be host, repository, or service", value.Parameters[i].Name)
		}
		if value.Parameters[i].Default, err = clean("parameter default", value.Parameters[i].Default, false); err != nil {
			return Procedure{}, err
		}
		if value.Parameters[i].Default != "" && value.Parameters[i].ResourceKind != "" {
			// A resource default is a reference, not a check that it exists:
			// records may arrive through sync in any order.
			value.Parameters[i].Default = strings.TrimPrefix(value.Parameters[i].Default, "resource:")
		}
		if parameterNames[value.Parameters[i].Name] {
			return Procedure{}, fmt.Errorf("duplicate parameter %q", value.Parameters[i].Name)
		}
		parameterNames[value.Parameters[i].Name] = true
	}
	if len(value.Steps) == 0 || len(value.Steps) > maxSteps {
		return Procedure{}, fmt.Errorf("steps must contain between 1 and %d items", maxSteps)
	}
	stepIDs, selectors := map[string]bool{}, map[string]bool{}
	for i := range value.Steps {
		step := &value.Steps[i]
		if step.ID, err = clean("step ID", step.ID, true); err != nil {
			return Procedure{}, err
		}
		if step.Description, err = clean("step description", step.Description, true); err != nil {
			return Procedure{}, err
		}
		if value.Kind == KindRunbook {
			// Runbook steps are situational actions, not completion stages.
			if step.StageKind != "" || step.StageID != "" {
				return Procedure{}, fmt.Errorf("runbook step %q cannot select a completion stage", step.ID)
			}
		} else {
			if step.StageKind, err = clean("step stage kind", step.StageKind, true); err != nil {
				return Procedure{}, err
			}
			if step.StageKind != "source_check" {
				return Procedure{}, fmt.Errorf("check step %q must select a source_check stage", step.ID)
			}
			if step.StageID, err = clean("step stage ID", step.StageID, true); err != nil {
				return Procedure{}, err
			}
		}
		if step.ArgvTemplate, err = normalizeSequence("argv template", step.ArgvTemplate); err != nil {
			return Procedure{}, err
		}
		selector := step.StageKind + ":" + step.StageID
		if stepIDs[step.ID] || value.Kind == KindCheck && selectors[selector] {
			return Procedure{}, fmt.Errorf("procedure step IDs and selectors must be unique")
		}
		stepIDs[step.ID], selectors[selector] = true, true
	}
	value.Authority, value.SchemaVersion = "caller_asserted", SchemaVersion
	return value, nil
}

func (s Service) Append(ctx context.Context, value Procedure) (Procedure, error) {
	value, err := normalizeProcedure(value)
	if err != nil {
		return Procedure{}, err
	}
	var next int
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM reviewed_procedures WHERE project_id=? AND procedure_id=?`, value.ProjectID, value.ID).Scan(&next); err != nil {
		return Procedure{}, err
	}
	if value.Revision == 0 {
		value.Revision = next
	}
	if value.Revision != next {
		return Procedure{}, fmt.Errorf("procedure revision must be the next revision %d", next)
	}
	value.RecordedAt = s.now()
	return value, Insert(ctx, s.DB, value)
}

func (s Service) Get(ctx context.Context, projectID, procedureID string, revision int) (*Procedure, error) {
	projectID, procedureID = strings.TrimSpace(projectID), strings.TrimSpace(procedureID)
	if projectID == "" || procedureID == "" {
		return nil, fmt.Errorf("project and procedure ID are required")
	}
	if revision < 0 {
		return nil, fmt.Errorf("procedure revision cannot be negative")
	}
	query := `SELECT ` + Columns + ` FROM reviewed_procedures WHERE project_id=? AND procedure_id=?`
	args := []any{projectID, procedureID}
	if revision > 0 {
		query += ` AND revision=?`
		args = append(args, revision)
	} else {
		query += ` AND status='accepted'`
	}
	query += ` ORDER BY revision DESC LIMIT 1`
	value := Procedure{ProjectID: projectID}
	err := scanProcedure(s.DB.QueryRowContext(ctx, query, args...), &value)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// ListOptions filters List. Empty values match everything.
type ListOptions struct {
	Kind   string
	Status string
	Limit  int
}

// MaxListLimit bounds List.
const MaxListLimit = 100

// List returns the latest revision of each procedure, newest first.
func (s Service) List(ctx context.Context, projectID string, options ListOptions) ([]Procedure, error) {
	if options.Kind != "" {
		kind, err := NormalizeKind(options.Kind)
		if err != nil {
			return nil, err
		}
		options.Kind = kind
	}
	if options.Status != "" && options.Status != "proposed" && options.Status != "accepted" && options.Status != "superseded" {
		return nil, fmt.Errorf("unsupported procedure status %q", options.Status)
	}
	if options.Limit == 0 {
		options.Limit = 50
	}
	if options.Limit < 1 || options.Limit > MaxListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", MaxListLimit)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT p.procedure_id, p.revision FROM reviewed_procedures p WHERE p.project_id=? AND p.revision=(SELECT MAX(revision) FROM reviewed_procedures WHERE project_id=p.project_id AND procedure_id=p.procedure_id) AND (?='' OR p.kind=?) AND (?='' OR p.status=?) ORDER BY p.recorded_at DESC, p.procedure_id LIMIT ?`, projectID, options.Kind, options.Kind, options.Status, options.Status, options.Limit)
	if err != nil {
		return nil, err
	}
	type key struct {
		id       string
		revision int
	}
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.revision); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]Procedure, 0, len(keys))
	for _, k := range keys {
		value, err := s.Get(ctx, projectID, k.id, k.revision)
		if err != nil {
			return nil, err
		}
		if value != nil {
			result = append(result, *value)
		}
	}
	return result, nil
}

// Latest returns the newest revision of a procedure whatever its status, which
// is what a person asking to see a procedure expects. Binding to completion
// profiles uses Get, where revision zero means the latest accepted revision.
func (s Service) Latest(ctx context.Context, projectID, procedureID string) (*Procedure, error) {
	projectID, procedureID = strings.TrimSpace(projectID), strings.TrimSpace(procedureID)
	if projectID == "" || procedureID == "" {
		return nil, fmt.Errorf("project and procedure ID are required")
	}
	value := Procedure{ProjectID: projectID}
	err := scanProcedure(s.DB.QueryRowContext(ctx, `SELECT `+Columns+` FROM reviewed_procedures WHERE project_id=? AND procedure_id=? ORDER BY revision DESC LIMIT 1`, projectID, procedureID), &value)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func StageIDs(value Procedure) []string {
	result := make([]string, len(value.Steps))
	for i, step := range value.Steps {
		result[i] = step.StageID
	}
	return result
}

// Validate returns value as Append would store it (cleaned, redacted, and with
// its kind normalized), or the reason it would be refused.
func Validate(value Procedure) (Procedure, error) {
	return normalizeProcedure(value)
}

// Execer is a database or transaction that can insert a procedure revision.
type Execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// Insert stores one already-validated procedure revision as is. Append and the
// portable importer both use it, so stored JSON is encoded one way.
func Insert(ctx context.Context, db Execer, value Procedure) error {
	platforms, _ := json.Marshal(value.Platforms)
	parameters, _ := json.Marshal(value.Parameters)
	effects, _ := json.Marshal(value.DeclaredEffects)
	validations, _ := json.Marshal(value.Validations)
	postRun, _ := json.Marshal(value.PostRunChecks)
	steps, _ := json.Marshal(value.Steps)
	_, err := db.ExecContext(ctx, `INSERT INTO reviewed_procedures(project_id,procedure_id,revision,title,purpose,kind,status,platforms_json,working_directory,parameters_json,declared_effects_json,validations_json,post_run_checks_json,steps_json,source,source_ref,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, value.ProjectID, value.ID, value.Revision, value.Title, value.Purpose, value.Kind, value.Status, string(platforms), value.WorkingDirectory, string(parameters), string(effects), string(validations), string(postRun), string(steps), value.Source, value.SourceRef, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), SchemaVersion)
	return err
}

// Scan decodes one row of the columns Get and the portable exporter select.
func scanProcedure(row interface{ Scan(...any) error }, value *Procedure) error {
	var platforms, parameters, effects, validations, postRun, steps, recorded string
	if err := row.Scan(&value.ID, &value.Revision, &value.Title, &value.Purpose, &value.Kind, &value.Status, &platforms, &value.WorkingDirectory, &parameters, &effects, &validations, &postRun, &steps, &value.Source, &value.SourceRef, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
		return err
	}
	for _, item := range []struct {
		body string
		to   any
	}{{platforms, &value.Platforms}, {parameters, &value.Parameters}, {effects, &value.DeclaredEffects}, {validations, &value.Validations}, {postRun, &value.PostRunChecks}, {steps, &value.Steps}} {
		if err := json.Unmarshal([]byte(item.body), item.to); err != nil {
			return err
		}
	}
	var err error
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return err
}

// Columns is the select list scanProcedure expects.
const Columns = `procedure_id,revision,title,purpose,kind,status,platforms_json,working_directory,parameters_json,declared_effects_json,validations_json,post_run_checks_json,steps_json,source,COALESCE(source_ref,''),authority,recorded_at,schema_version`

// Rows is a result set of Columns.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// ScanAll decodes every row of Columns for projectID.
func ScanAll(rows Rows, projectID string) ([]Procedure, error) {
	defer rows.Close()
	var result []Procedure
	for rows.Next() {
		value := Procedure{ProjectID: projectID}
		if err := scanProcedure(rows, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
