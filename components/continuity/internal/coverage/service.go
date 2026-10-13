package coverage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	MaxBytes     = 16 << 10
	maxRefs      = 32
	DefaultLimit = 50
	MaxLimit     = 200
)

type Service struct {
	DB   *sql.DB
	Root string // project root; requirements paths are relative to it
	Now  func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func clean(name, v string, required bool) (string, error) {
	v = strings.TrimSpace(redact.String(v))
	if required && v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(v) > MaxBytes {
		return "", fmt.Errorf("%s exceeds 16 KiB limit", name)
	}
	return v, nil
}

func oneOf(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// Validate checks and normalizes a cell without writing it.
func Validate(c Cell) (Cell, error) {
	var err error
	for _, field := range []struct {
		name     string
		target   *string
		required bool
	}{
		{"requirement ID", &c.RequirementID, true},
		{"client", &c.Client, true}, {"platform", &c.Platform, true},
		{"client version", &c.ClientVersion, false}, {"status", &c.Status, true},
		{"verification", &c.Verification, true}, {"implementation", &c.Implementation, false},
		{"creator", &c.Creator, true}, {"model", &c.Model, false},
	} {
		if *field.target, err = clean(field.name, *field.target, field.required); err != nil {
			return Cell{}, err
		}
	}
	if !oneOf(c.Status, Statuses) {
		return Cell{}, fmt.Errorf("unsupported coverage status %q (unsupported, partial, supported, or failing)", c.Status)
	}
	if !oneOf(c.Verification, Verifications) {
		return Cell{}, fmt.Errorf("unsupported verification %q (inferred, reported, observed, or tested)", c.Verification)
	}
	if len(c.Refs) > maxRefs {
		return Cell{}, fmt.Errorf("coverage cells accept at most %d refs", maxRefs)
	}
	refs := make([]string, 0, len(c.Refs))
	seen := map[string]bool{}
	for _, ref := range c.Refs {
		if ref, err = clean("ref", ref, true); err != nil {
			return Cell{}, err
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	c.Refs = refs
	if c.Authority == "" {
		c.Authority = "caller_asserted"
	}
	if !oneOf(c.Authority, []string{"caller_asserted", "user_reported", "model_suggested"}) {
		return Cell{}, fmt.Errorf("unsupported coverage authority %q", c.Authority)
	}
	if c.ID == "" {
		c.ID = activity.NewID()
	}
	if c.ID, err = clean("cell ID", c.ID, true); err != nil {
		return Cell{}, err
	}
	c.SchemaVersion = SchemaVersion
	return c, nil
}

// Record appends one cell observed by this project's instance. The
// requirement must exist in the requirements file, so a typo cannot create an
// orphan.
func (s Service) Record(ctx context.Context, requirementsPath string, c Cell) (Cell, error) {
	if strings.TrimSpace(c.ProjectID) == "" {
		return Cell{}, fmt.Errorf("project ID is required")
	}
	c, err := Validate(c)
	if err != nil {
		return Cell{}, err
	}
	reqs, problems := LoadRequirements(s.Root, requirementsPath)
	found := false
	for _, r := range reqs {
		found = found || r.ID == c.RequirementID
	}
	if !found {
		detail := ""
		if len(problems) > 0 {
			detail = " (" + strings.Join(problems, "; ") + ")"
		}
		return Cell{}, fmt.Errorf("requirement %q is not in the requirements file%s", c.RequirementID, detail)
	}
	if c.InstanceID, err = s.LocalInstance(ctx, c.ProjectID); err != nil {
		return Cell{}, err
	}
	c.RecordedAt = s.now()
	if err := Insert(ctx, s.DB, c); err != nil {
		return Cell{}, err
	}
	return c, nil
}

// LocalInstance returns this project's instance ID, or "" before the project
// has a portable identity.
func (s Service) LocalInstance(ctx context.Context, projectID string) (string, error) {
	var value string
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT instance_id FROM project_memory_identities WHERE project_id=?),'')`, projectID).Scan(&value)
	return value, err
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Insert appends a validated cell as given, including its instance and time.
// Import uses it for cells observed on other machines, whose requirement may
// not be in this checkout's requirements file yet.
func Insert(ctx context.Context, db execer, c Cell) error {
	refs, err := json.Marshal(c.Refs)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO coverage_cells(id, project_id, requirement_id, client, platform, client_version, status, verification, implementation, refs_json, creator, model, authority, instance_id, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?)`,
		c.ID, c.ProjectID, c.RequirementID, c.Client, c.Platform, c.ClientVersion, c.Status, c.Verification, c.Implementation, string(refs), c.Creator, c.Model, c.Authority, c.InstanceID, c.RecordedAt.UTC().Format(time.RFC3339Nano), c.SchemaVersion)
	return err
}

// All returns every cell of a project in recorded order, for export.
func All(ctx context.Context, db queryer, projectID string) ([]Cell, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+cellColumns+` FROM coverage_cells WHERE project_id=? ORDER BY recorded_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cells []Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

const cellColumns = `id, project_id, requirement_id, client, platform, COALESCE(client_version,''), status, verification, COALESCE(implementation,''), refs_json, creator, COALESCE(model,''), authority, COALESCE(instance_id,''), recorded_at, schema_version`

func scanCell(row interface{ Scan(...any) error }) (Cell, error) {
	var c Cell
	var refs, at string
	if err := row.Scan(&c.ID, &c.ProjectID, &c.RequirementID, &c.Client, &c.Platform, &c.ClientVersion, &c.Status, &c.Verification, &c.Implementation, &refs, &c.Creator, &c.Model, &c.Authority, &c.InstanceID, &at, &c.SchemaVersion); err != nil {
		return Cell{}, err
	}
	if err := json.Unmarshal([]byte(refs), &c.Refs); err != nil {
		return Cell{}, err
	}
	var err error
	c.RecordedAt, err = time.Parse(time.RFC3339Nano, at)
	return c, err
}

// Current returns the latest cell of each (requirement, client, platform),
// ordered by requirement, client, then platform.
func (s Service) Current(ctx context.Context, projectID string) ([]Cell, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cellColumns+` FROM coverage_cells c WHERE project_id=? AND NOT EXISTS (SELECT 1 FROM coverage_cells n WHERE n.project_id=c.project_id AND n.requirement_id=c.requirement_id AND n.client=c.client AND n.platform=c.platform AND (n.recorded_at>c.recorded_at OR (n.recorded_at=c.recorded_at AND n.id>c.id))) ORDER BY requirement_id, client, platform`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cells []Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

// History returns every cell of one requirement, newest first.
func (s Service) History(ctx context.Context, projectID, requirementID string) ([]Cell, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cellColumns+` FROM coverage_cells WHERE project_id=? AND requirement_id=? ORDER BY recorded_at DESC, id DESC`, projectID, requirementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cells []Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, rows.Err()
}

var statusRank = map[string]int{"failing": 0, "unsupported": 1, "partial": 2, "supported": 3}

func rowStatus(cells []Cell) string {
	if len(cells) == 0 {
		return "unknown"
	}
	worst := cells[0].Status
	for _, c := range cells[1:] {
		if statusRank[c.Status] < statusRank[worst] {
			worst = c.Status
		}
	}
	return worst
}

// Options narrows a report. Empty fields match everything.
type Options struct {
	RequirementsPath string
	Requirement      string
	Client           string
	Platform         string
	Status           string
	Offset           int
	Limit            int
}

// Report builds the matrix: every requirement in the file with its current
// cells, cells whose requirement is gone, and parse problems.
func (s Service) Report(ctx context.Context, projectID string, opts Options) (Report, error) {
	path := opts.RequirementsPath
	if path == "" {
		path = DefaultRequirementsPath
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	reqs, problems := LoadRequirements(s.Root, path)
	cells, err := s.Current(ctx, projectID)
	if err != nil {
		return Report{}, err
	}
	instance, err := s.LocalInstance(ctx, projectID)
	if err != nil {
		return Report{}, err
	}
	byReq := map[string][]Cell{}
	for _, c := range cells {
		byReq[c.RequirementID] = append(byReq[c.RequirementID], c)
	}
	report := Report{SchemaVersion: SchemaVersion, RequirementsPath: path, InstanceID: instance, Rows: []Row{}, Orphaned: []Cell{}, Problems: append([]string{}, problems...),
		Authority: "caller_asserted", Disclaimer: Disclaimer}
	known := map[string]bool{}
	var matched []Row
	for _, r := range reqs {
		known[r.ID] = true
		if opts.Requirement != "" && opts.Requirement != r.ID {
			continue
		}
		var keep []Cell
		for _, c := range byReq[r.ID] {
			if (opts.Client == "" || c.Client == opts.Client) && (opts.Platform == "" || c.Platform == opts.Platform) {
				keep = append(keep, c)
			}
		}
		row := Row{Requirement: r, Cells: keep, Status: rowStatus(keep)}
		if row.Cells == nil {
			row.Cells = []Cell{}
		}
		if opts.Status != "" && row.Status != opts.Status {
			continue
		}
		matched = append(matched, row)
	}
	for _, c := range cells {
		if !known[c.RequirementID] && (opts.Requirement == "" || opts.Requirement == c.RequirementID) {
			report.Orphaned = append(report.Orphaned, c)
		}
	}
	report.Total = len(matched)
	if opts.Offset < len(matched) {
		matched = matched[opts.Offset:]
	} else {
		matched = nil
	}
	if len(matched) > limit {
		matched, report.Truncated = matched[:limit], true
	}
	if matched != nil {
		report.Rows = matched
	}
	report.Returned = len(report.Rows)
	return report, nil
}

// FindStale returns current cells that name a client version when the version
// most recently observed for that client differs. installed maps a client
// name to that version; clients missing from it are skipped, and a cell
// without a version is never stale.
func FindStale(cells []Cell, installed map[string]string) []Stale {
	var stale []Stale
	for _, c := range cells {
		if now, ok := installed[c.Client]; ok && now != "" && c.ClientVersion != "" && c.ClientVersion != now {
			stale = append(stale, Stale{Cell: c, Installed: now})
		}
	}
	return stale
}

// InstalledVersions returns the newest observed client_version per client
// from recorded actor invocations.
func (s Service) InstalledVersions(ctx context.Context, projectID string) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT client, client_version FROM actor_invocations a WHERE project_id=? AND client IS NOT NULL AND client_version IS NOT NULL AND client_version<>'' AND NOT EXISTS (SELECT 1 FROM actor_invocations n WHERE n.project_id=a.project_id AND n.client=a.client AND n.client_version IS NOT NULL AND n.client_version<>'' AND (n.occurred_at>a.occurred_at OR (n.occurred_at=a.occurred_at AND n.id>a.id)))`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := map[string]string{}
	for rows.Next() {
		var client, version string
		if err := rows.Scan(&client, &version); err != nil {
			return nil, err
		}
		versions[client] = version
	}
	return versions, rows.Err()
}

// StaleCells returns the current cells whose verified client version differs
// from the version most recently observed for their client.
func (s Service) StaleCells(ctx context.Context, projectID string) ([]Stale, error) {
	cells, err := s.Current(ctx, projectID)
	if err != nil {
		return nil, err
	}
	installed, err := s.InstalledVersions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return FindStale(cells, installed), nil
}
