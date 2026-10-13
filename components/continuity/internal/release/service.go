package release

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	MaxEntries            = 64
	MaxNarrativesPerEntry = 16
	DefaultLimit          = 20
	MaxLimit              = 100
)

type Service struct {
	DB   *sql.DB
	Root string
	Now  func() time.Time
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
	if len(value) > 16<<10 {
		return "", fmt.Errorf("%s exceeds 16 KiB limit", name)
	}
	return value, nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s Service) Append(ctx context.Context, value Record) (Record, error) {
	var err error
	if value.ProjectID, err = clean("project ID", value.ProjectID, true); err != nil {
		return Record{}, err
	}
	if value.ID, err = clean("release ID", value.ID, true); err != nil {
		return Record{}, err
	}
	if value.Version, err = clean("version", value.Version, true); err != nil {
		return Record{}, err
	}
	if value.Title, err = clean("title", value.Title, true); err != nil {
		return Record{}, err
	}
	if value.Reviewer, err = clean("reviewer", value.Reviewer, false); err != nil {
		return Record{}, err
	}
	if !oneOf(value.Status, "draft", "reviewed", "published", "superseded") {
		return Record{}, fmt.Errorf("unsupported release status %q", value.Status)
	}
	if value.ID == "unreleased" && value.Status != "draft" {
		return Record{}, fmt.Errorf("the unreleased record must remain draft")
	}
	if value.ID != "unreleased" && value.Version == "Unreleased" {
		return Record{}, fmt.Errorf("only release ID unreleased may use version Unreleased")
	}
	if oneOf(value.Status, "reviewed", "published") && value.Reviewer == "" {
		return Record{}, fmt.Errorf("reviewer is required for %s releases", value.Status)
	}
	if value.Status == "published" && value.PublishedAt == nil {
		return Record{}, fmt.Errorf("published time is required for published releases")
	}
	if value.Status != "published" && value.PublishedAt != nil {
		return Record{}, fmt.Errorf("published time is supported only for published releases")
	}
	if len(value.Entries) == 0 || len(value.Entries) > MaxEntries {
		return Record{}, fmt.Errorf("release entries must contain 1 to %d items", MaxEntries)
	}
	for i := range value.Entries {
		entry := &value.Entries[i]
		entry.Position = i + 1
		if !oneOf(entry.Category, "feature", "fix", "migration", "operational", "breaking") {
			return Record{}, fmt.Errorf("unsupported release entry category %q", entry.Category)
		}
		if entry.Wording, err = clean("entry wording", entry.Wording, true); err != nil {
			return Record{}, err
		}
		if entry.NodeID, err = clean("entry node ID", entry.NodeID, true); err != nil {
			return Record{}, err
		}
		if entry.NodeRevision < 1 {
			return Record{}, fmt.Errorf("entry node revision must be positive")
		}
		if len(entry.NarrativeIDs) == 0 || len(entry.NarrativeIDs) > MaxNarrativesPerEntry {
			return Record{}, fmt.Errorf("entry narratives must contain 1 to %d IDs", MaxNarrativesPerEntry)
		}
		for j := range entry.NarrativeIDs {
			if entry.NarrativeIDs[j], err = clean("narrative ID", entry.NarrativeIDs[j], true); err != nil {
				return Record{}, err
			}
		}
		sort.Strings(entry.NarrativeIDs)
		entry.NarrativeIDs = unique(entry.NarrativeIDs)
		var status string
		if err := s.DB.QueryRowContext(ctx, `SELECT status FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, value.ProjectID, entry.NodeID, entry.NodeRevision).Scan(&status); err != nil {
			if err == sql.ErrNoRows {
				return Record{}, fmt.Errorf("roadmap node %s@%d is not recorded", entry.NodeID, entry.NodeRevision)
			}
			return Record{}, err
		}
		if status != "completed" {
			return Record{}, fmt.Errorf("roadmap node %s@%d is %s, not completed", entry.NodeID, entry.NodeRevision, status)
		}
		for _, id := range entry.NarrativeIDs {
			var count int
			if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=? AND id=?`, value.ProjectID, id).Scan(&count); err != nil {
				return Record{}, err
			}
			if count == 0 {
				return Record{}, fmt.Errorf("work narrative %s is not recorded", id)
			}
		}
	}
	value.ContentSHA256, err = ContentHash(value)
	if err != nil {
		return Record{}, err
	}
	if value.Revision == 0 {
		if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM roadmap_releases WHERE project_id=? AND release_id=?`, value.ProjectID, value.ID).Scan(&value.Revision); err != nil {
			return Record{}, err
		}
	}
	if value.Revision < 1 {
		return Record{}, fmt.Errorf("release revision must be positive")
	}
	if value.Status == "published" {
		var previousStatus, previousHash string
		err := s.DB.QueryRowContext(ctx, `SELECT status,content_sha256 FROM roadmap_releases WHERE project_id=? AND release_id=? ORDER BY revision DESC LIMIT 1`, value.ProjectID, value.ID).Scan(&previousStatus, &previousHash)
		if err != nil {
			return Record{}, fmt.Errorf("published release requires a prior reviewed revision")
		}
		if previousStatus != "reviewed" || previousHash != value.ContentSHA256 {
			return Record{}, fmt.Errorf("published release must exactly match its latest reviewed revision")
		}
	}
	value.Authority, value.RecordedAt, value.SchemaVersion = "caller_asserted", s.now(), SchemaVersion
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	var published any
	if value.PublishedAt != nil {
		published = value.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO roadmap_releases(project_id,release_id,revision,version,title,status,reviewer,published_at,content_sha256,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,NULLIF(?,''),?,?,?,?,?)`, value.ProjectID, value.ID, value.Revision, value.Version, value.Title, value.Status, value.Reviewer, published, value.ContentSHA256, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
		return Record{}, err
	}
	for _, entry := range value.Entries {
		narratives, _ := json.Marshal(entry.NarrativeIDs)
		if _, err = tx.ExecContext(ctx, `INSERT INTO roadmap_release_entries(project_id,release_id,release_revision,position,category,wording,node_id,node_revision,narrative_ids_json) VALUES(?,?,?,?,?,?,?,?,?)`, value.ProjectID, value.ID, value.Revision, entry.Position, entry.Category, entry.Wording, entry.NodeID, entry.NodeRevision, string(narratives)); err != nil {
			return Record{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return value, nil
}

// ContentHash identifies the exact reviewed wording and evidence selection.
// Lifecycle fields such as status, reviewer, and publication time are excluded.
func ContentHash(value Record) (string, error) {
	body, err := json.Marshal(struct {
		Version string  `json:"version"`
		Title   string  `json:"title"`
		Entries []Entry `json:"entries"`
	}{value.Version, value.Title, value.Entries})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func unique(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func (s Service) History(ctx context.Context, projectID, id string, limit int) (View, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return View{}, fmt.Errorf("release limit must be between 1 and %d", MaxLimit)
	}
	query := `SELECT release_id,revision,version,title,status,COALESCE(reviewer,''),published_at,content_sha256,authority,recorded_at,schema_version FROM roadmap_releases WHERE project_id=?`
	countQuery := `SELECT COUNT(*) FROM roadmap_releases WHERE project_id=?`
	args := []any{projectID}
	countArgs := []any{projectID}
	if id != "" {
		query += ` AND release_id=?`
		args = append(args, id)
		countQuery += ` AND release_id=?`
		countArgs = append(countArgs, id)
	}
	query += ` ORDER BY recorded_at DESC,release_id,revision DESC LIMIT ?`
	args = append(args, limit)
	var total int
	if err := s.DB.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return View{}, err
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return View{}, err
	}
	defer rows.Close()
	values := make([]Record, 0, limit)
	for rows.Next() {
		value, err := scanRecord(rows)
		if err != nil {
			return View{}, err
		}
		value.ProjectID = projectID
		value.Entries, err = s.entries(ctx, projectID, value.ID, value.Revision)
		if err != nil {
			return View{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return View{}, err
	}
	truncated := total > len(values)
	return View{SchemaVersion: 1, ProjectID: projectID, Releases: values, Total: total, Limit: limit, Truncated: truncated, Authority: "mixed", Disclaimer: "Release records are caller-reviewed selections; rendering is deterministic and does not publish them."}, nil
}

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (Record, error) {
	var value Record
	var published sql.NullString
	var recorded string
	err := row.Scan(&value.ID, &value.Revision, &value.Version, &value.Title, &value.Status, &value.Reviewer, &published, &value.ContentSHA256, &value.Authority, &recorded, &value.SchemaVersion)
	if err != nil {
		return Record{}, err
	}
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return Record{}, err
	}
	if published.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, published.String)
		if parseErr != nil {
			return Record{}, parseErr
		}
		value.PublishedAt = &parsed
	}
	return value, nil
}

func (s Service) entries(ctx context.Context, projectID, id string, revision int) ([]Entry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT position,category,wording,node_id,node_revision,narrative_ids_json FROM roadmap_release_entries WHERE project_id=? AND release_id=? AND release_revision=? ORDER BY position`, projectID, id, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Entry{}
	for rows.Next() {
		var value Entry
		var narratives string
		if err := rows.Scan(&value.Position, &value.Category, &value.Wording, &value.NodeID, &value.NodeRevision, &narratives); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(narratives), &value.NarrativeIDs); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s Service) latest(ctx context.Context, projectID, id string) (*Record, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT release_id,revision,version,title,status,COALESCE(reviewer,''),published_at,content_sha256,authority,recorded_at,schema_version FROM roadmap_releases WHERE project_id=? AND release_id=? ORDER BY revision DESC LIMIT 1`, projectID, id)
	value, err := scanRecord(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value.ProjectID = projectID
	value.Entries, err = s.entries(ctx, projectID, id, value.Revision)
	return &value, err
}

func (s Service) Render(ctx context.Context, projectID string) (Projection, error) {
	unreleased, err := s.latest(ctx, projectID, "unreleased")
	if err != nil {
		return Projection{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `WITH latest AS (SELECT release_id,MAX(revision) revision FROM roadmap_releases WHERE project_id=? AND release_id<>'unreleased' GROUP BY release_id) SELECT r.release_id,r.revision,r.version,r.title,r.status,COALESCE(r.reviewer,''),r.published_at,r.content_sha256,r.authority,r.recorded_at,r.schema_version FROM roadmap_releases r JOIN latest l ON l.release_id=r.release_id AND l.revision=r.revision WHERE r.project_id=? AND r.status='published' ORDER BY r.published_at DESC,r.release_id`, projectID, projectID)
	if err != nil {
		return Projection{}, err
	}
	defer rows.Close()
	releases := []Record{}
	for rows.Next() {
		value, scanErr := scanRecord(rows)
		if scanErr != nil {
			return Projection{}, scanErr
		}
		value.ProjectID = projectID
		value.Entries, scanErr = s.entries(ctx, projectID, value.ID, value.Revision)
		if scanErr != nil {
			return Projection{}, scanErr
		}
		releases = append(releases, value)
	}
	if err := rows.Err(); err != nil {
		return Projection{}, err
	}
	var out strings.Builder
	out.WriteString("# Changelog\n\n")
	if unreleased != nil {
		renderRecord(&out, *unreleased)
	} else {
		out.WriteString("## Unreleased\n\n_No reviewed draft entries._\n\n")
	}
	for _, value := range releases {
		renderRecord(&out, value)
	}
	markdown := out.String()
	sum := sha256.Sum256([]byte(markdown))
	return Projection{SchemaVersion: 1, ProjectID: projectID, Markdown: markdown, SHA256: hex.EncodeToString(sum[:]), Authority: "mixed", Draft: true, Disclaimer: "Deterministic draft projection only; publication requires an explicit reviewed release transition."}, nil
}

func renderRecord(out *strings.Builder, value Record) {
	out.WriteString("## " + value.Version + "\n\n")
	groups := []string{"breaking", "feature", "fix", "migration", "operational"}
	labels := map[string]string{"breaking": "Breaking", "feature": "Features", "fix": "Fixes", "migration": "Migrations", "operational": "Operational"}
	for _, kind := range groups {
		wrote := false
		for _, entry := range value.Entries {
			if entry.Category != kind {
				continue
			}
			if !wrote {
				out.WriteString("### " + labels[kind] + "\n\n")
				wrote = true
			}
			out.WriteString("- " + entry.Wording + "\n")
		}
		if wrote {
			out.WriteString("\n")
		}
	}
}

func (s Service) Check(ctx context.Context, projectID, path string) (ProjectionCheck, error) {
	projection, err := s.Render(ctx, projectID)
	if err != nil {
		return ProjectionCheck{}, err
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return ProjectionCheck{}, fmt.Errorf("projection path must be project-relative")
	}
	result := ProjectionCheck{SchemaVersion: 1, ProjectID: projectID, Path: filepath.ToSlash(path), State: "missing", ExpectedHash: projection.SHA256, Authority: "machine_observed", Disclaimer: "Hash comparison only; this command does not write or publish the changelog."}
	body, err := os.ReadFile(filepath.Join(s.Root, path))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return ProjectionCheck{}, err
	}
	sum := sha256.Sum256(body)
	result.ActualHash = hex.EncodeToString(sum[:])
	if result.ActualHash == result.ExpectedHash {
		result.State = "current"
	} else {
		result.State = "changed"
	}
	return result, nil
}
