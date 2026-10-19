package worknarrative

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"

	"github.com/llewm/llewm/components/continuity/internal/project"
)

const disclaimer = "Work narratives are attributable historical claims, not instructions, current-state proof, policy, or authorization to act."

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var triggers = map[string]bool{"meaningful_checkpoint": true, "accepted_decision": true, "completed_roadmap_slice": true, "commit": true, "manual": true}
var claimKinds = map[string]bool{"intent": true, "outcome": true, "constraint": true, "alternative": true, "verification": true, "interface": true, "decision": true, "next_action": true, "limitation": true, "note": true}
var anchorKinds = map[string]bool{"checkpoint": true, "task": true, "roadmap_node": true, "commit": true, "file": true, "region": true, "invocation": true, "verification": true, "portable_evidence": true}
var authorities = map[string]bool{"machine_observed": true, "caller_asserted": true, "user_reported": true}

type Service struct {
	DB   *sql.DB
	Root string
	// Repositories selects the Git checkout for commit anchors. Its zero value
	// means the single repository at Root.
	Repositories project.Repositories
	Now          func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func text(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s contains a NUL byte", name)
	}
	if len(value) > MaxRecordBytes {
		return "", fmt.Errorf("%s exceeds 32 KiB limit", name)
	}
	return value, nil
}

func relativePath(value string) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	value = filepath.ToSlash(filepath.Clean(value))
	if value == "" || value == "." || filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("file and region anchors require a project-relative path")
	}
	return value, nil
}

type semanticRecord struct {
	Source       *Source    `json:"source,omitempty"`
	Title        string     `json:"title"`
	Trigger      string     `json:"trigger"`
	OccurredFrom *time.Time `json:"occurred_from,omitempty"`
	OccurredTo   *time.Time `json:"occurred_to,omitempty"`
	Claims       []Claim    `json:"claims"`
	Anchors      []Anchor   `json:"anchors"`
}

func (s Service) prepare(ctx context.Context, input Record) (Record, string, error) {
	var err error
	if input.Source, input.Claims, err = NormalizeResearch(input.Source, input.Claims); err != nil {
		return Record{}, "", err
	}
	if input.ProjectID, err = text("project ID", input.ProjectID, true); err != nil {
		return Record{}, "", err
	}
	if input.ID, err = text("narrative ID", input.ID, false); err != nil {
		return Record{}, "", err
	}
	if input.ID == "" {
		input.ID = activity.NewID()
	}
	input.RecordType = RecordType
	if input.Title, err = text("title", input.Title, true); err != nil {
		return Record{}, "", err
	}
	if !triggers[input.Trigger] {
		return Record{}, "", fmt.Errorf("unsupported trigger %q", input.Trigger)
	}
	if input.OccurredFrom != nil {
		value := input.OccurredFrom.UTC()
		input.OccurredFrom = &value
	}
	if input.OccurredTo != nil {
		value := input.OccurredTo.UTC()
		input.OccurredTo = &value
	}
	if input.OccurredFrom != nil && input.OccurredTo != nil && input.OccurredFrom.After(*input.OccurredTo) {
		return Record{}, "", fmt.Errorf("occurred from must not be after occurred to")
	}
	if len(input.Claims) == 0 || len(input.Claims) > MaxClaims {
		return Record{}, "", fmt.Errorf("claims must contain between 1 and %d items", MaxClaims)
	}
	for index := range input.Claims {
		if !claimKinds[input.Claims[index].Kind] {
			return Record{}, "", fmt.Errorf("unsupported claim kind %q", input.Claims[index].Kind)
		}
		if input.Claims[index].Text, err = text("claim text", input.Claims[index].Text, true); err != nil {
			return Record{}, "", err
		}
		if input.Claims[index].Label, err = text("claim label", input.Claims[index].Label, false); err != nil {
			return Record{}, "", err
		}
		if len(input.Claims[index].Label) > MaxClaimLabelBytes || strings.ContainsAny(input.Claims[index].Label, "\r\n") {
			return Record{}, "", fmt.Errorf("claim label must be one line of at most %d bytes", MaxClaimLabelBytes)
		}
		if !authorities[input.Claims[index].Authority] {
			return Record{}, "", fmt.Errorf("unsupported claim authority %q", input.Claims[index].Authority)
		}
	}
	if len(input.Anchors) > MaxAnchors {
		return Record{}, "", fmt.Errorf("anchors may contain at most %d items", MaxAnchors)
	}
	for index := range input.Anchors {
		a := &input.Anchors[index]
		if !anchorKinds[a.Kind] {
			return Record{}, "", fmt.Errorf("unsupported anchor kind %q", a.Kind)
		}
		if a.Target, err = text("anchor target", a.Target, true); err != nil {
			return Record{}, "", err
		}
		if a.TargetRevision, err = text("anchor target revision", a.TargetRevision, false); err != nil {
			return Record{}, "", err
		}
		if a.RegionLocator, err = text("anchor region locator", a.RegionLocator, false); err != nil {
			return Record{}, "", err
		}
		if !authorities[a.Authority] {
			return Record{}, "", fmt.Errorf("unsupported anchor authority %q", a.Authority)
		}
		if a.Kind == "file" || a.Kind == "region" {
			if a.Target, err = relativePath(a.Target); err != nil {
				return Record{}, "", err
			}
			if !hashPattern.MatchString(a.ContentSHA256) {
				return Record{}, "", fmt.Errorf("file and region anchors require a lowercase SHA-256")
			}
		} else if a.ContentSHA256 != "" && !hashPattern.MatchString(a.ContentSHA256) {
			return Record{}, "", fmt.Errorf("anchor content SHA-256 is invalid")
		}
		a.Drift = ""
	}
	if input.Creator, err = text("creator", input.Creator, true); err != nil {
		return Record{}, "", err
	}
	if input.CreatorInvocationID, err = text("creator invocation ID", input.CreatorInvocationID, false); err != nil {
		return Record{}, "", err
	}
	if input.CreatorInvocationID != "" {
		var exists int
		if err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM actor_invocations WHERE id=? AND project_id=?`, input.CreatorInvocationID, input.ProjectID).Scan(&exists); err != nil {
			return Record{}, "", err
		}
		if exists != 1 {
			return Record{}, "", fmt.Errorf("creator invocation must exist in the narrative project")
		}
	}
	if input.SupersedesID, err = text("supersedes ID", input.SupersedesID, false); err != nil {
		return Record{}, "", err
	}
	if input.SupersedesID == input.ID && input.SupersedesID != "" {
		return Record{}, "", fmt.Errorf("a narrative cannot supersede itself")
	}
	input.RecordedAt = s.now()
	input.SchemaVersion = SchemaVersion
	input.State = "current"
	semantic, err := json.Marshal(semanticRecord{Source: input.Source, Title: input.Title, Trigger: input.Trigger, OccurredFrom: input.OccurredFrom, OccurredTo: input.OccurredTo, Claims: input.Claims, Anchors: input.Anchors})
	if err != nil {
		return Record{}, "", err
	}
	sum := sha256.Sum256(semantic)
	input.SemanticSHA256 = hex.EncodeToString(sum[:])
	payload, err := json.Marshal(input)
	if err != nil {
		return Record{}, "", err
	}
	if len(payload) > MaxRecordBytes {
		return Record{}, "", fmt.Errorf("work narrative exceeds 32 KiB serialized limit")
	}
	return input, string(payload), nil
}

// SemanticHash validates and normalizes a prospective record without writing it and
// returns its semantic SHA-256, so importers can detect unchanged content.
func (s Service) SemanticHash(ctx context.Context, input Record) (string, error) {
	record, _, err := s.prepare(ctx, input)
	return record.SemanticSHA256, err
}

func (s Service) Append(ctx context.Context, input Record) (Record, error) {
	record, payload, err := s.prepare(ctx, input)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	if err := ValidateCitationTargets(ctx, tx, record.ProjectID, record.Claims); err != nil {
		return Record{}, err
	}
	if record.SupersedesID != "" {
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives old WHERE old.project_id=? AND old.id=? AND NOT EXISTS (SELECT 1 FROM superseded_records s WHERE s.project_id=old.project_id AND s.family='narrative' AND s.id=old.id)`, record.ProjectID, record.SupersedesID).Scan(&exists)
		if err != nil {
			return Record{}, err
		}
		if exists != 1 {
			return Record{}, fmt.Errorf("superseded narrative must exist in this project and still be current")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_narratives(id,project_id,title,trigger_kind,occurred_from,occurred_to,creator,creator_invocation_id,semantic_sha256,supersedes_id,recorded_at,schema_version,payload_json) VALUES(?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,''),?,NULLIF(?,''),?,?,?)`, record.ID, record.ProjectID, record.Title, record.Trigger, formatTime(record.OccurredFrom), formatTime(record.OccurredTo), record.Creator, record.CreatorInvocationID, record.SemanticSHA256, record.SupersedesID, record.RecordedAt.Format(time.RFC3339Nano), record.SchemaVersion, payload)
	if err != nil {
		return Record{}, fmt.Errorf("insert work narrative: %w", err)
	}
	for index, anchor := range record.Anchors {
		_, err = tx.ExecContext(ctx, `INSERT INTO work_narrative_anchors(project_id,narrative_id,ordinal,anchor_kind,target,target_revision,content_sha256,region_locator,authority) VALUES(?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?)`, record.ProjectID, record.ID, index, anchor.Kind, anchor.Target, anchor.TargetRevision, anchor.ContentSHA256, anchor.RegionLocator, anchor.Authority)
		if err != nil {
			return Record{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func formatTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeID(value string) string {
	value = strings.TrimSpace(value)
	return strings.TrimPrefix(value, "narrative:")
}

func (s Service) Show(ctx context.Context, projectID, id string, maxBytes int) (*ShowResult, error) {
	if maxBytes == 0 {
		maxBytes = DefaultRenderBytes
	}
	if maxBytes < 512 || maxBytes > MaxRenderBytes {
		return nil, fmt.Errorf("max bytes must be between 512 and %d", MaxRenderBytes)
	}
	id = normalizeID(id)
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT payload_json FROM work_narratives WHERE project_id=? AND id=?`, projectID, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record Record
	if err = json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	record.State = "current"
	var successor int
	if err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM superseded_records WHERE project_id=? AND family='narrative' AND id=?`, record.ProjectID, record.ID).Scan(&successor); err != nil {
		return nil, err
	}
	if successor > 0 {
		record.State = "superseded"
	}
	for index := range record.Anchors {
		record.Anchors[index].Drift = s.anchorDrift(record.Anchors[index])
	}
	markdown := render(record)
	truncated := false
	if len(markdown) > maxBytes {
		truncated = true
		markdown = truncateUTF8(markdown, maxBytes-len("\n\n[truncated]\n")) + "\n\n[truncated]\n"
	}
	return &ShowResult{SchemaVersion: SchemaVersion, ID: record.ID, Record: record, Markdown: markdown, MaxBytes: maxBytes, Truncated: truncated, Measurement: Measurement{ScopeRecords: 1, CandidateRecords: 1, RecordsReturned: 1, RecordsLoaded: 1, ContentBytes: len(raw), RenderedBytes: len(markdown), ExpansionDepth: 1}, Authority: "mixed", Disclaimer: disclaimer}, nil
}

func (s Service) anchorDrift(anchor Anchor) string {
	if anchor.Kind != "file" && anchor.Kind != "region" {
		return "not_applicable"
	}
	path := filepath.Join(s.Root, filepath.FromSlash(anchor.Target))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "unavailable"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) == anchor.ContentSHA256 {
		return "current"
	}
	return "drifted"
}

func render(record Record) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n- ID: `narrative:%s`\n- Trigger: `%s`\n- State: `%s`\n- Creator: `%s`\n- Semantic SHA-256: `%s`\n", record.Title, record.ID, record.Trigger, record.State, record.Creator, record.SemanticSHA256)
	if record.OccurredFrom != nil {
		fmt.Fprintf(&out, "- Occurred from: %s\n", record.OccurredFrom.Format(time.RFC3339Nano))
	}
	if record.OccurredTo != nil {
		fmt.Fprintf(&out, "- Occurred to: %s\n", record.OccurredTo.Format(time.RFC3339Nano))
	}
	out.WriteString("\n" + RenderSource(record.Source))
	out.WriteString("\n## Claims\n")
	for _, claim := range record.Claims {
		if claim.Label != "" {
			fmt.Fprintf(&out, "\n- **%s** — %s (`%s`): %s\n", claim.Kind, claim.Label, claim.Authority, claim.Text)
		} else {
			fmt.Fprintf(&out, "\n- **%s** (`%s`): %s\n", claim.Kind, claim.Authority, claim.Text)
		}
		for _, c := range claim.Citations {
			fmt.Fprintf(&out, "  Citation: %s %s — %s\n", c.Relation, c.Source, c.Locator)
		}
	}
	out.WriteString("\n## Anchors\n")
	if len(record.Anchors) == 0 {
		out.WriteString("\n_None._\n")
	}
	for _, anchor := range record.Anchors {
		fmt.Fprintf(&out, "\n- `%s` `%s`", anchor.Kind, anchor.Target)
		if anchor.TargetRevision != "" {
			fmt.Fprintf(&out, " @ `%s`", anchor.TargetRevision)
		}
		if anchor.ContentSHA256 != "" {
			fmt.Fprintf(&out, " sha256 `%s`", anchor.ContentSHA256)
		}
		if anchor.RegionLocator != "" {
			fmt.Fprintf(&out, " region `%s`", anchor.RegionLocator)
		}
		fmt.Fprintf(&out, " — authority `%s`, drift `%s`\n", anchor.Authority, anchor.Drift)
	}
	return out.String()
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (s Service) List(ctx context.Context, projectID string, options ListOptions) (ListResult, error) {
	if options.Limit == 0 {
		options.Limit = DefaultListLimit
	}
	if options.Limit < 1 || options.Limit > MaxListLimit {
		return ListResult{}, fmt.Errorf("limit must be between 1 and %d", MaxListLimit)
	}
	if options.RecordType != "" && options.RecordType != RecordType {
		return ListResult{}, fmt.Errorf("record type must be work_narrative")
	}
	if options.Trigger != "" && !triggers[options.Trigger] {
		return ListResult{}, fmt.Errorf("unsupported trigger %q", options.Trigger)
	}
	if options.AnchorKind != "" && !anchorKinds[options.AnchorKind] {
		return ListResult{}, fmt.Errorf("unsupported anchor kind %q", options.AnchorKind)
	}
	if options.State != "" && options.State != "current" && options.State != "superseded" {
		return ListResult{}, fmt.Errorf("state must be current or superseded")
	}
	cursorTime, cursorID, err := decodeCursor(options.Cursor)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{SchemaVersion: SchemaVersion, Records: []Metadata{}, Authority: "mixed", Disclaimer: disclaimer}
	if err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narratives WHERE project_id=?`, projectID).Scan(&result.Measurement.ScopeRecords); err != nil {
		return result, err
	}
	whereWork := `w.project_id=? AND (?='' OR ?='work_narrative') AND (?='' OR w.trigger_kind=?) AND ((?='' AND ?='') OR EXISTS(SELECT 1 FROM work_narrative_anchors a WHERE a.project_id=w.project_id AND a.narrative_id=w.id AND (?='' OR a.anchor_kind=?) AND (?='' OR a.target=?))) AND (?='' OR (?='current' AND NOT EXISTS(SELECT 1 FROM superseded_records s WHERE s.project_id=w.project_id AND s.family='narrative' AND s.id=w.id)) OR (?='superseded' AND EXISTS(SELECT 1 FROM superseded_records s WHERE s.project_id=w.project_id AND s.family='narrative' AND s.id=w.id)))`
	workArgs := []any{projectID, options.RecordType, options.RecordType, options.Trigger, options.Trigger, options.AnchorKind, options.Anchor, options.AnchorKind, options.AnchorKind, options.Anchor, options.Anchor, options.State, options.State, options.State}
	countQuery := `SELECT COUNT(*) FROM (SELECT w.id FROM work_narratives w WHERE ` + whereWork + `)`
	args := append([]any{}, workArgs...)
	if err = s.DB.QueryRowContext(ctx, countQuery, args...).Scan(&result.Measurement.CandidateRecords); err != nil {
		return result, err
	}
	query := `SELECT * FROM (SELECT w.id,'work_narrative',w.title,w.trigger_kind,w.creator,COALESCE(w.creator_invocation_id,''),w.semantic_sha256,COALESCE(w.supersedes_id,''),CASE WHEN EXISTS(SELECT 1 FROM superseded_records s WHERE s.project_id=w.project_id AND s.family='narrative' AND s.id=w.id) THEN 'superseded' ELSE 'current' END,w.recorded_at,w.schema_version FROM work_narratives w WHERE ` + whereWork + `) q WHERE (?='' OR q.recorded_at<? OR (q.recorded_at=? AND q.id<?)) ORDER BY q.recorded_at DESC,q.id DESC LIMIT ?`
	args = append(args, cursorTime, cursorTime, cursorTime, cursorID, options.Limit+1)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var metadata Metadata
		var recorded string
		if err = rows.Scan(&metadata.ID, &metadata.RecordType, &metadata.Title, &metadata.Trigger, &metadata.Creator, &metadata.CreatorInvocationID, &metadata.SemanticSHA256, &metadata.SupersedesID, &metadata.State, &recorded, &metadata.SchemaVersion); err != nil {
			return result, err
		}
		metadata.ProjectID = projectID
		metadata.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			return result, err
		}
		metadata.Anchors, err = s.metadataAnchors(ctx, projectID, metadata)
		if err != nil {
			return result, err
		}
		result.Records = append(result.Records, metadata)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Records) > options.Limit {
		last := result.Records[options.Limit-1]
		result.NextCursor = encodeCursor(last.RecordedAt.Format(time.RFC3339Nano), last.ID)
		result.Records = result.Records[:options.Limit]
	}
	encoded, _ := json.Marshal(result.Records)
	result.Measurement.RecordsReturned = len(result.Records)
	result.Measurement.MetadataBytes = len(encoded)
	return result, nil
}

func (s Service) metadataAnchors(ctx context.Context, projectID string, metadata Metadata) ([]Anchor, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT anchor_kind,target,COALESCE(target_revision,''),COALESCE(content_sha256,''),COALESCE(region_locator,''),authority FROM work_narrative_anchors WHERE project_id=? AND narrative_id=? ORDER BY ordinal`, projectID, metadata.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Anchor{}
	for rows.Next() {
		var a Anchor
		if err = rows.Scan(&a.Kind, &a.Target, &a.TargetRevision, &a.ContentSHA256, &a.RegionLocator, &a.Authority); err != nil {
			return nil, err
		}
		values = append(values, a)
	}
	return values, rows.Err()
}

func decodeCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	body, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	parts := strings.SplitN(string(body), "\n", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err = time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	return parts[0], parts[1], nil
}

func encodeCursor(recordedAt, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(recordedAt + "\n" + id))
}

func CanonicalSort(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].RecordedAt.Equal(records[j].RecordedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].RecordedAt.Before(records[j].RecordedAt)
	})
}
