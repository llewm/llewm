package decision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision/decisionhash"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	maxTitleBytes     = 256
	maxRationaleBytes = 8 << 10
	maxLinks          = 16
	maxIDBytes        = 256
	maxShowBytes      = 64 << 10
)

const readDisclaimer = "Caller-asserted decisions and exact links are informational. Reads do not select work, change linked records, grant instruction authority, or authorize execution."

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

func oneOf(v string, choices ...string) bool {
	for _, choice := range choices {
		if v == choice {
			return true
		}
	}
	return false
}

func clean(name, value string, max int, required, singleLine bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > max {
		return "", fmt.Errorf("%s exceeds %d-byte limit", name, max)
	}
	if singleLine && strings.ContainsAny(value, "\r\n\t") {
		return "", fmt.Errorf("%s must be one line", name)
	}
	return value, nil
}

func cleanID(name, value string, required bool) (string, error) {
	value, err := clean(name, value, maxIDBytes, required, true)
	if err != nil {
		return "", err
	}
	for _, r := range value {
		if r > 127 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return "", fmt.Errorf("%s must use letters, digits, dot, underscore, or hyphen", name)
		}
	}
	return value, nil
}

func normalizeDecisionID(value string) string { return strings.TrimPrefix(value, "decision:") }

func validateTarget(kind, id, revision string) (string, string, string, error) {
	kind = strings.TrimSpace(kind)
	if !oneOf(kind, "task", "checkpoint", "roadmap_node", "track_plan", "work_item", "narrative", "commit", "decision") {
		return "", "", "", fmt.Errorf("unsupported target kind %q", kind)
	}
	var err error
	id, err = clean("target ID", id, maxIDBytes, true, true)
	if err != nil {
		return "", "", "", err
	}
	revision, err = clean("target revision", revision, 128, false, true)
	if err != nil {
		return "", "", "", err
	}
	if kind == "decision" && revision != "" {
		return "", "", "", errors.New("decision targets have no revision")
	}
	return kind, id, revision, nil
}

func normalize(v Record) (Record, error) {
	var err error
	v.ID, err = cleanID("decision ID", normalizeDecisionID(v.ID), true)
	if err != nil {
		return Record{}, err
	}
	v.ProjectID, err = clean("project ID", v.ProjectID, maxIDBytes, true, true)
	if err != nil {
		return Record{}, err
	}
	v.Title, err = clean("title", v.Title, maxTitleBytes, true, true)
	if err != nil {
		return Record{}, err
	}
	v.Rationale, err = clean("rationale", v.Rationale, maxRationaleBytes, true, false)
	if err != nil {
		return Record{}, err
	}
	v.Actor, err = clean("actor", v.Actor, maxIDBytes, true, true)
	if err != nil {
		return Record{}, err
	}
	v.ReviewSource, err = clean("review source", v.ReviewSource, maxIDBytes, false, true)
	if err != nil {
		return Record{}, err
	}
	v.SupersedesID, err = cleanID("superseded decision ID", normalizeDecisionID(v.SupersedesID), false)
	if err != nil {
		return Record{}, err
	}
	if v.ID == v.SupersedesID {
		return Record{}, errors.New("decision cannot supersede itself")
	}
	if !oneOf(v.Disposition, "accepted", "deferred", "rejected", "superseded") {
		return Record{}, fmt.Errorf("unsupported disposition %q", v.Disposition)
	}
	if v.ActorType != "human" {
		return Record{}, errors.New("first decision ledger requires an explicit human actor claim")
	}
	if !oneOf(v.Authority, "caller_asserted", "user_reported") {
		return Record{}, fmt.Errorf("unsupported authority %q", v.Authority)
	}
	if v.Authority == "user_reported" && v.ReviewSource == "" {
		return Record{}, errors.New("user-reported decision requires an explicit review source")
	}
	if len(v.Links) > maxLinks {
		return Record{}, fmt.Errorf("decision exceeds %d links", maxLinks)
	}
	if v.Links == nil {
		v.Links = []Link{}
	}
	seen := map[string]bool{}
	for i := range v.Links {
		link := &v.Links[i]
		if !oneOf(link.Kind, "subject", "supports", "opposes", "resulted_in", "supersedes") {
			return Record{}, fmt.Errorf("unsupported link kind %q", link.Kind)
		}
		link.TargetKind, link.TargetID, link.TargetRevision, err = validateTarget(link.TargetKind, link.TargetID, link.TargetRevision)
		if err != nil {
			return Record{}, err
		}
		if link.Authority == "" {
			link.Authority = "caller_asserted"
		}
		if link.Authority != "caller_asserted" {
			return Record{}, errors.New("decision links must retain caller-asserted authority")
		}
		key := link.Kind + "\x00" + link.TargetKind + "\x00" + link.TargetID + "\x00" + link.TargetRevision
		if seen[key] {
			return Record{}, errors.New("duplicate decision link")
		}
		seen[key] = true
	}
	v.SchemaVersion = SchemaVersion
	v.RecordedAt = time.Time{}
	v.RecordSHA256 = ""
	v.Replay = false
	v.SupersededBy = ""
	return v, nil
}

// SemanticHash is the project-scoped record_sha256 of a decision and its links.
func SemanticHash(v Record) (string, error) { return semanticHash(v) }

func semanticHash(v Record) (string, error) {
	return decisionhash.Hash(decisionhash.Fields{ID: v.ID, ProjectID: v.ProjectID, Title: v.Title, Rationale: v.Rationale, Disposition: v.Disposition, Actor: v.Actor, ActorType: v.ActorType, Authority: v.Authority, ReviewSource: v.ReviewSource, SupersedesID: v.SupersedesID, Links: v.Links})
}

func (s Service) Append(ctx context.Context, input Record) (Record, error) {
	v, err := normalize(input)
	if err != nil {
		return Record{}, err
	}
	v.RecordSHA256, err = semanticHash(v)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT record_sha256 FROM decision_records WHERE project_id=? AND id=?`, v.ProjectID, v.ID).Scan(&existing)
	if err == nil {
		if existing != v.RecordSHA256 {
			return Record{}, fmt.Errorf("decision %q conflicts with existing record", v.ID)
		}
		if err = tx.Commit(); err != nil {
			return Record{}, err
		}
		result, err := s.Show(ctx, v.ProjectID, v.ID, 0)
		result.Replay = true
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	if v.SupersedesID != "" {
		var found string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM decision_records WHERE project_id=? AND id=?`, v.ProjectID, v.SupersedesID).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Record{}, fmt.Errorf("superseded decision %q is missing in this project", v.SupersedesID)
			}
			return Record{}, err
		}
		var superseded bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM superseded_records WHERE project_id=? AND family='decision' AND id=?)`, v.ProjectID, v.SupersedesID).Scan(&superseded); err != nil {
			return Record{}, err
		}
		if superseded {
			return Record{}, fmt.Errorf("decision %q is already superseded; supersede its current successor instead", v.SupersedesID)
		}
	}
	v.RecordedAt = s.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,supersedes_id,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ProjectID, v.RecordSHA256, v.Title, v.Rationale, v.Disposition, v.Actor, v.ActorType, v.Authority, v.ReviewSource, nullable(v.SupersedesID), v.RecordedAt.Format(time.RFC3339Nano), SchemaVersion)
	if err != nil {
		return Record{}, err
	}
	for i, link := range v.Links {
		_, err = tx.ExecContext(ctx, `INSERT INTO decision_links(project_id,decision_id,ordinal,kind,target_kind,target_id,target_revision,authority) VALUES(?,?,?,?,?,?,?,?)`, v.ProjectID, v.ID, i, link.Kind, link.TargetKind, link.TargetID, link.TargetRevision, link.Authority)
		if err != nil {
			return Record{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return v, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func pageBounds(limit, offset int) (int, int, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, errors.New("limit must be 1-100 and offset must be nonnegative")
	}
	return limit, offset, nil
}

func (s Service) Show(ctx context.Context, projectID, id string, maxBytes int) (Record, error) {
	id = normalizeDecisionID(id)
	if maxBytes == 0 {
		maxBytes = 32 << 10
	}
	if maxBytes < 512 || maxBytes > maxShowBytes {
		return Record{}, errors.New("max bytes must be 512-65536")
	}
	v := Record{ProjectID: projectID, Links: []Link{}}
	var recorded string
	var supersedes, successor sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,supersedes_id,recorded_at,schema_version,`+SupersededBy("decision_records")+` FROM decision_records WHERE project_id=? AND id=?`, projectID, id).Scan(&v.ID, &v.RecordSHA256, &v.Title, &v.Rationale, &v.Disposition, &v.Actor, &v.ActorType, &v.Authority, &v.ReviewSource, &supersedes, &recorded, &v.SchemaVersion, &successor)
	if err != nil {
		return Record{}, err
	}
	v.SupersedesID = supersedes.String
	v.SupersededBy = successor.String
	v.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	if err != nil {
		return Record{}, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT kind,target_kind,target_id,target_revision,authority FROM decision_links WHERE project_id=? AND decision_id=? ORDER BY ordinal`, projectID, id)
	if err != nil {
		return Record{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var link Link
		if err = rows.Scan(&link.Kind, &link.TargetKind, &link.TargetID, &link.TargetRevision, &link.Authority); err != nil {
			return Record{}, err
		}
		v.Links = append(v.Links, link)
	}
	if err = rows.Err(); err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return Record{}, err
	}
	if len(body) > maxBytes {
		return Record{}, fmt.Errorf("decision show exceeds %d-byte limit", maxBytes)
	}
	return v, nil
}

func (s Service) List(ctx context.Context, projectID string, options ListOptions) (ListPage, error) {
	limit, offset, err := pageBounds(options.Limit, options.Offset)
	if err != nil {
		return ListPage{}, err
	}
	if options.Disposition != "" && !oneOf(options.Disposition, "accepted", "deferred", "rejected", "superseded") {
		return ListPage{}, errors.New("invalid disposition filter")
	}
	if options.TargetKind != "" || options.TargetID != "" || options.TargetRevision != "" {
		options.TargetKind, options.TargetID, options.TargetRevision, err = validateTarget(options.TargetKind, options.TargetID, options.TargetRevision)
		if err != nil {
			return ListPage{}, err
		}
	}
	where := `d.project_id=?`
	args := []any{projectID}
	if options.Disposition != "" {
		where += ` AND d.disposition=?`
		args = append(args, options.Disposition)
	}
	if options.TargetKind != "" {
		where += ` AND EXISTS (SELECT 1 FROM decision_links l WHERE l.project_id=d.project_id AND l.decision_id=d.id AND l.target_kind=? AND l.target_id=? AND l.target_revision=?)`
		args = append(args, options.TargetKind, options.TargetID, options.TargetRevision)
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ListPage{}, err
	}
	defer tx.Rollback()
	page := ListPage{SchemaVersion: SchemaVersion, Decisions: []Summary{}, Limit: limit, Offset: offset, BodiesLoaded: 0, Authority: "caller_asserted", Disclaimer: readDisclaimer}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_records d WHERE `+where, args...).Scan(&page.Total); err != nil {
		return ListPage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.title,d.disposition,d.actor,d.authority,d.recorded_at,d.schema_version,d.supersedes_id,`+SupersededBy("d")+`,(SELECT COUNT(*) FROM decision_links l WHERE l.project_id=d.project_id AND l.decision_id=d.id) FROM decision_records d WHERE `+where+` ORDER BY d.recorded_at DESC,d.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return ListPage{}, err
	}
	for rows.Next() {
		var item Summary
		var recorded string
		var predecessor, successor sql.NullString
		if err = rows.Scan(&item.ID, &item.Title, &item.Disposition, &item.Actor, &item.Authority, &recorded, &item.SchemaVersion, &predecessor, &successor, &item.LinkCount); err != nil {
			rows.Close()
			return ListPage{}, err
		}
		item.SupersedesID = predecessor.String
		item.SupersededBy = successor.String
		item.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			rows.Close()
			return ListPage{}, err
		}
		page.Decisions = append(page.Decisions, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return ListPage{}, err
	}
	if err = rows.Close(); err != nil {
		return ListPage{}, err
	}
	page.Truncated = offset+len(page.Decisions) < page.Total
	if err = tx.Commit(); err != nil {
		return ListPage{}, err
	}
	return page, nil
}

func (s Service) Backlinks(ctx context.Context, projectID, targetKind, targetID, targetRevision string, limit, offset int) (BacklinkPage, error) {
	var err error
	targetKind, targetID, targetRevision, err = validateTarget(targetKind, targetID, targetRevision)
	if err != nil {
		return BacklinkPage{}, err
	}
	limit, offset, err = pageBounds(limit, offset)
	if err != nil {
		return BacklinkPage{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BacklinkPage{}, err
	}
	defer tx.Rollback()
	page := BacklinkPage{SchemaVersion: SchemaVersion, IncludedFamilies: []string{"decision", "narrative", "work_item", "track_plan"}, Backlinks: []Backlink{}, Limit: limit, Offset: offset, BodiesLoaded: 0, Authority: "mixed", Disclaimer: readDisclaimer + " Included families and indexed target kinds are documented; absent relationships are not implied."}
	// Every arm is an exact stored relationship. Without a target revision, links to
	// any revision match and each row reports the exact revision it names. Source
	// bodies and free-text refs are excluded. The common ordering makes offsets
	// stable on an unchanged DB.
	parts := []string{`SELECT 'decision' source_kind,d.id source_id,'' source_revision,l.kind link_kind,'' link_status,l.target_kind,l.target_id,l.target_revision,l.authority,d.recorded_at,CASE WHEN l.target_kind='decision' AND target.id IS NOT NULL THEN 'resolved' WHEN l.target_kind='decision' THEN 'missing' ELSE 'unverified' END resolution, l.ordinal ordinal FROM decision_links l JOIN decision_records d ON d.project_id=l.project_id AND d.id=l.decision_id LEFT JOIN decision_records target ON target.project_id=l.project_id AND target.id=l.target_id AND l.target_kind='decision' WHERE l.project_id=? AND l.target_kind=? AND l.target_id=? AND (?='' OR l.target_revision=?)`}
	args := []any{projectID, targetKind, targetID, targetRevision, targetRevision}
	if targetKind == "task" || targetKind == "checkpoint" || targetKind == "roadmap_node" || targetKind == "commit" {
		parts = append(parts, `SELECT 'narrative',n.id,'',a.anchor_kind,'',a.anchor_kind,a.target,COALESCE(a.target_revision,''),a.authority,n.recorded_at,'unverified',a.ordinal FROM work_narrative_anchors a JOIN work_narratives n ON n.project_id=a.project_id AND n.id=a.narrative_id WHERE a.project_id=? AND a.anchor_kind=? AND a.target=? AND (?='' OR COALESCE(a.target_revision,'')=?)`)
		args = append(args, projectID, targetKind, targetID, targetRevision, targetRevision)
	}
	if targetKind == "roadmap_node" {
		if targetRevision == "" {
			parts = append(parts, `SELECT 'work_item',w.id,'','context','','roadmap_node',w.roadmap_node_id,'',w.authority,w.recorded_at,'unverified',0 FROM work_items w WHERE w.project_id=? AND w.roadmap_node_id=?`)
			args = append(args, projectID, targetID)
			parts = append(parts, `SELECT 'track_plan',p.id,CAST(p.revision AS TEXT),'scope','','roadmap_node',p.roadmap_node_id,'',p.authority,p.recorded_at,'unverified',0 FROM track_plan_revisions p WHERE p.project_id=? AND p.roadmap_node_id=?`)
			args = append(args, projectID, targetID)
		}
	}
	if targetRevision == "" {
		var column string
		switch targetKind {
		case "task":
			column = "task_id"
		case "checkpoint":
			column = "checkpoint_id"
		}
		if column != "" {
			parts = append(parts, `SELECT 'work_item',w.id,'','context','',?,w.`+column+`,'',w.authority,w.recorded_at,'unverified',0 FROM work_items w WHERE w.project_id=? AND w.`+column+`=?`)
			args = append(args, targetKind, projectID, targetID)
		}
		if targetKind == "task" {
			parts = append(parts, `SELECT 'track_plan',p.id,CAST(p.revision AS TEXT),'scope','','task',p.task_id,'',p.authority,p.recorded_at,'unverified',0 FROM track_plan_revisions p WHERE p.project_id=? AND p.task_id=?`)
			args = append(args, projectID, targetID)
		}
		if targetKind == "work_item" {
			parts = append(parts, `SELECT 'track_plan',p.id,CAST(p.revision AS TEXT),'includes_work_item','','work_item',pw.item_id,'',p.authority,p.recorded_at,'unverified',0 FROM track_plan_work_items pw JOIN track_plan_revisions p ON p.project_id=pw.project_id AND p.id=pw.plan_id AND p.revision=pw.plan_revision WHERE pw.project_id=? AND pw.item_id=?`)
			args = append(args, projectID, targetID)
		}
	}
	query := `WITH links AS (` + strings.Join(parts, ` UNION ALL `) + `) `
	if err = tx.QueryRowContext(ctx, query+`SELECT COUNT(*) FROM links`, args...).Scan(&page.Total); err != nil {
		return BacklinkPage{}, err
	}
	rows, err := tx.QueryContext(ctx, query+`SELECT source_kind,source_id,source_revision,link_kind,link_status,target_kind,target_id,target_revision,authority,recorded_at,resolution FROM links ORDER BY recorded_at DESC,source_kind,source_id,source_revision,ordinal LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return BacklinkPage{}, err
	}
	for rows.Next() {
		var item Backlink
		var recorded string
		if err = rows.Scan(&item.SourceKind, &item.SourceID, &item.SourceRevision, &item.LinkKind, &item.LinkStatus, &item.TargetKind, &item.TargetID, &item.TargetRevision, &item.Authority, &recorded, &item.TargetResolution); err != nil {
			rows.Close()
			return BacklinkPage{}, err
		}
		item.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		if err != nil {
			rows.Close()
			return BacklinkPage{}, err
		}
		page.Backlinks = append(page.Backlinks, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return BacklinkPage{}, err
	}
	if err = rows.Close(); err != nil {
		return BacklinkPage{}, err
	}
	page.Truncated = offset+len(page.Backlinks) < page.Total
	if err = tx.Commit(); err != nil {
		return BacklinkPage{}, err
	}
	return page, nil
}

// supersededBy is the SQL for the decision that replaces the row named by
// alias: its newest successor that no fork resolution rejected, else the head a
// resolution accepted over it, else its newest successor.
// SupersededBy is a SQL expression for the current successor of the decision
// row aliased alias: an unrejected successor, else the fork-accepted head.
func SupersededBy(alias string) string {
	successor := `SELECT s.id FROM decision_records s WHERE s.project_id=` + alias + `.project_id AND s.supersedes_id=` + alias + `.id`
	return `COALESCE((` + successor + ` AND NOT EXISTS(SELECT 1 FROM fork_rejected_records r WHERE r.project_id=s.project_id AND r.family='decision' AND r.id=s.id) ORDER BY s.recorded_at DESC,s.id DESC LIMIT 1),` +
		`(SELECT r.accepted_id FROM fork_rejected_records r WHERE r.project_id=` + alias + `.project_id AND r.family='decision' AND r.id=` + alias + `.id ORDER BY r.recorded_at DESC LIMIT 1),` +
		`(` + successor + ` ORDER BY s.recorded_at DESC,s.id DESC LIMIT 1))`
}
