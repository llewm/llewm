// Package topic records curated history topics, their aliases, and explicit links.
// Topics are navigational metadata: they never change linked records, select work,
// or grant instruction or execution authority.
package topic

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
	SchemaVersion   = 1
	maxIDBytes      = 128
	maxTitleBytes   = 256
	maxSummaryBytes = 4 << 10
	maxLinksShown   = 200
	Disclaimer      = "Topics and topic links are caller-asserted navigation metadata. They do not change linked records, select work, or grant instruction or execution authority."
)

var TargetKinds = []string{"roadmap_node", "narrative", "narrative_claim", "decision", "work_item", "track_plan", "task", "checkpoint", "commit", "file"}

type Service struct {
	DB  *sql.DB
	Now func() time.Time
}

type Topic struct {
	ProjectID     string    `json:"project_id"`
	ID            string    `json:"id"`
	Revision      int       `json:"revision"`
	Title         string    `json:"title"`
	Summary       string    `json:"summary"`
	Status        string    `json:"status"`
	Creator       string    `json:"creator"`
	Model         string    `json:"model,omitempty"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

type Link struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	TopicID        string    `json:"topic_id"`
	TargetKind     string    `json:"target_kind"`
	TargetID       string    `json:"target_id"`
	TargetRevision string    `json:"target_revision,omitempty"`
	Status         string    `json:"status"`
	Creator        string    `json:"creator"`
	Model          string    `json:"model,omitempty"`
	Authority      string    `json:"authority"`
	AcceptedFrom   string    `json:"accepted_from,omitempty"`
	RecordedAt     time.Time `json:"recorded_at"`
}

type Summary struct {
	Topic
	Aliases       []string `json:"aliases"`
	AcceptedLinks int      `json:"accepted_links"`
	ProposedLinks int      `json:"proposed_links"`
}

type Detail struct {
	SchemaVersion  int      `json:"schema_version"`
	Topic          Topic    `json:"topic"`
	Aliases        []string `json:"aliases"`
	Links          []Link   `json:"links"`
	LinksTruncated bool     `json:"links_truncated"`
	Authority      string   `json:"authority"`
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

// NormalizeID accepts letters, digits, dot, underscore, and hyphen, and strips a topic: prefix.
func NormalizeID(name, value string) (string, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "topic:")
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maxIDBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", name, maxIDBytes)
	}
	for _, r := range value {
		if r > 127 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return "", fmt.Errorf("%s must use letters, digits, dot, underscore, or hyphen", name)
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

// authority enforces that model-created records stay proposed and model-suggested.
func authority(model, status, value string) (string, error) {
	if value == "" {
		value = "caller_asserted"
		if model != "" {
			value = "model_suggested"
		}
	}
	if !oneOf(value, "caller_asserted", "user_reported", "model_suggested") {
		return "", fmt.Errorf("unsupported authority %q", value)
	}
	if model != "" && (status != "proposed" || value != "model_suggested") {
		return "", errors.New("model-created topics and links must be proposed with model_suggested authority")
	}
	return value, nil
}

// Record appends the next revision of a topic.
func (s Service) Record(ctx context.Context, value Topic) (Topic, error) {
	var err error
	if value.ID, err = NormalizeID("topic ID", value.ID); err != nil {
		return Topic{}, err
	}
	if value.Title, err = bounded("title", value.Title, maxTitleBytes, true); err != nil {
		return Topic{}, err
	}
	if value.Summary, err = bounded("summary", value.Summary, maxSummaryBytes, false); err != nil {
		return Topic{}, err
	}
	if value.Creator, err = bounded("creator", value.Creator, maxIDBytes, true); err != nil {
		return Topic{}, err
	}
	if value.Status == "" {
		value.Status = "accepted"
		if value.Model != "" {
			value.Status = "proposed"
		}
	}
	if !oneOf(value.Status, "proposed", "accepted", "retired") {
		return Topic{}, fmt.Errorf("unsupported topic status %q", value.Status)
	}
	if value.Authority, err = authority(value.Model, value.Status, value.Authority); err != nil {
		return Topic{}, err
	}
	value.RecordedAt, value.SchemaVersion = s.now(), SchemaVersion
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Topic{}, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM topic_revisions WHERE project_id=? AND id=?`, value.ProjectID, value.ID).Scan(&value.Revision); err != nil {
		return Topic{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO topic_revisions(project_id,id,revision,title,summary,status,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`, value.ProjectID, value.ID, value.Revision, value.Title, value.Summary, value.Status, value.Creator, value.Model, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion); err != nil {
		return Topic{}, err
	}
	return value, tx.Commit()
}

// Alias maps a lowercase alias to one topic. Aliases are unique per project.
func (s Service) Alias(ctx context.Context, projectID, topicID, alias, creator string) error {
	var err error
	if topicID, err = NormalizeID("topic ID", topicID); err != nil {
		return err
	}
	if alias, err = NormalizeID("alias", strings.ToLower(alias)); err != nil {
		return err
	}
	if creator, err = bounded("creator", creator, maxIDBytes, true); err != nil {
		return err
	}
	if _, err = s.latest(ctx, s.DB, projectID, topicID); err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO topic_aliases(project_id,alias,topic_id,creator,recorded_at) VALUES(?,?,?,?,?)`, projectID, alias, topicID, creator, s.now().Format(time.RFC3339Nano))
	return err
}

// AddLink appends a topic link. Accepting a proposal appends a new accepted row.
func (s Service) AddLink(ctx context.Context, value Link) (Link, error) {
	var err error
	if value.TopicID, err = NormalizeID("topic ID", value.TopicID); err != nil {
		return Link{}, err
	}
	if !oneOf(value.TargetKind, TargetKinds...) {
		return Link{}, fmt.Errorf("unsupported target kind %q", value.TargetKind)
	}
	if value.TargetID, err = bounded("target ID", value.TargetID, 512, true); err != nil {
		return Link{}, err
	}
	if value.TargetRevision, err = bounded("target revision", value.TargetRevision, 128, false); err != nil {
		return Link{}, err
	}
	if value.Creator, err = bounded("creator", value.Creator, maxIDBytes, true); err != nil {
		return Link{}, err
	}
	if value.Status == "" {
		value.Status = "accepted"
		if value.Model != "" {
			value.Status = "proposed"
		}
	}
	if !oneOf(value.Status, "proposed", "accepted", "retired") {
		return Link{}, fmt.Errorf("unsupported link status %q", value.Status)
	}
	if value.Authority, err = authority(value.Model, value.Status, value.Authority); err != nil {
		return Link{}, err
	}
	if _, err = s.latest(ctx, s.DB, value.ProjectID, value.TopicID); err != nil {
		return Link{}, err
	}
	value.ID, value.RecordedAt = activity.NewID(), s.now()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO topic_links(id,project_id,topic_id,target_kind,target_id,target_revision,status,creator,model,authority,accepted_from,recorded_at) VALUES(?,?,?,?,?,?,?,?,NULLIF(?,''),?,NULLIF(?,''),?)`, value.ID, value.ProjectID, value.TopicID, value.TargetKind, value.TargetID, value.TargetRevision, value.Status, value.Creator, value.Model, value.Authority, value.AcceptedFrom, value.RecordedAt.Format(time.RFC3339Nano))
	return value, err
}

// AcceptLink copies a proposed link into a new accepted row that names its source.
func (s Service) AcceptLink(ctx context.Context, projectID, linkID, creator string) (Link, error) {
	var link Link
	err := s.DB.QueryRowContext(ctx, `SELECT topic_id,target_kind,target_id,target_revision,status FROM topic_links WHERE project_id=? AND id=?`, projectID, linkID).Scan(&link.TopicID, &link.TargetKind, &link.TargetID, &link.TargetRevision, &link.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Link{}, fmt.Errorf("topic link %q not found", linkID)
	}
	if err != nil {
		return Link{}, err
	}
	if link.Status != "proposed" {
		return Link{}, errors.New("topic link is not proposed")
	}
	link.ProjectID, link.Status, link.Creator, link.AcceptedFrom = projectID, "accepted", creator, linkID
	return s.AddLink(ctx, link)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s Service) latest(ctx context.Context, db queryer, projectID, id string) (Topic, error) {
	var value Topic
	var model sql.NullString
	var recorded string
	err := db.QueryRowContext(ctx, `SELECT id,revision,title,summary,status,creator,model,authority,recorded_at,schema_version FROM topic_revisions WHERE project_id=? AND id=? ORDER BY revision DESC LIMIT 1`, projectID, id).Scan(&value.ID, &value.Revision, &value.Title, &value.Summary, &value.Status, &value.Creator, &model, &value.Authority, &recorded, &value.SchemaVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Topic{}, fmt.Errorf("topic %q not found", id)
	}
	if err != nil {
		return Topic{}, err
	}
	value.ProjectID, value.Model = projectID, model.String
	value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
	return value, err
}

// Resolve maps a topic ID or alias to a topic ID. It reports false when neither exists.
func Resolve(ctx context.Context, db queryer, projectID, value string) (string, bool, error) {
	id := strings.TrimPrefix(strings.TrimSpace(value), "topic:")
	var found string
	err := db.QueryRowContext(ctx, `SELECT id FROM topic_revisions WHERE project_id=? AND id=? UNION SELECT topic_id FROM topic_aliases WHERE project_id=? AND alias=? LIMIT 1`, projectID, id, projectID, strings.ToLower(id)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return found, err == nil, err
}

// CurrentLinks returns the latest row for each distinct target of a topic.
func CurrentLinks(ctx context.Context, db queryer, projectID, topicID string, limit int) ([]Link, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,topic_id,target_kind,target_id,target_revision,status,creator,COALESCE(model,''),authority,COALESCE(accepted_from,''),recorded_at FROM (
		SELECT l.*, ROW_NUMBER() OVER (PARTITION BY target_kind,target_id,target_revision ORDER BY recorded_at DESC,id DESC) rn FROM topic_links l WHERE project_id=? AND topic_id=?
	) WHERE rn=1 ORDER BY target_kind,target_id,target_revision LIMIT ?`, projectID, topicID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		var recorded string
		if err = rows.Scan(&link.ID, &link.TopicID, &link.TargetKind, &link.TargetID, &link.TargetRevision, &link.Status, &link.Creator, &link.Model, &link.Authority, &link.AcceptedFrom, &recorded); err != nil {
			return nil, false, err
		}
		link.ProjectID = projectID
		if link.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, false, err
		}
		links = append(links, link)
	}
	truncated := len(links) > limit
	if truncated {
		links = links[:limit]
	}
	return links, truncated, rows.Err()
}

func aliases(ctx context.Context, db queryer, projectID, topicID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT alias FROM topic_aliases WHERE project_id=? AND topic_id=? ORDER BY alias`, projectID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var alias string
		if err = rows.Scan(&alias); err != nil {
			return nil, err
		}
		values = append(values, alias)
	}
	return values, rows.Err()
}

// Show returns a topic by ID or alias with its aliases and current links.
func (s Service) Show(ctx context.Context, projectID, idOrAlias string) (Detail, error) {
	id, ok, err := Resolve(ctx, s.DB, projectID, idOrAlias)
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, fmt.Errorf("topic %q not found", idOrAlias)
	}
	detail := Detail{SchemaVersion: SchemaVersion, Authority: "caller_asserted", Disclaimer: Disclaimer}
	if detail.Topic, err = s.latest(ctx, s.DB, projectID, id); err != nil {
		return Detail{}, err
	}
	if detail.Aliases, err = aliases(ctx, s.DB, projectID, id); err != nil {
		return Detail{}, err
	}
	detail.Links, detail.LinksTruncated, err = CurrentLinks(ctx, s.DB, projectID, id, maxLinksShown)
	if detail.Links == nil {
		detail.Links = []Link{}
	}
	return detail, err
}

// List returns the latest revision of every topic with alias and link counts.
func (s Service) List(ctx context.Context, projectID string) ([]Summary, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT id FROM topic_revisions WHERE project_id=? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	summaries := []Summary{}
	for _, id := range ids {
		var summary Summary
		if summary.Topic, err = s.latest(ctx, s.DB, projectID, id); err != nil {
			return nil, err
		}
		if summary.Aliases, err = aliases(ctx, s.DB, projectID, id); err != nil {
			return nil, err
		}
		links, _, err := CurrentLinks(ctx, s.DB, projectID, id, maxLinksShown)
		if err != nil {
			return nil, err
		}
		for _, link := range links {
			switch link.Status {
			case "accepted":
				summary.AcceptedLinks++
			case "proposed":
				summary.ProposedLinks++
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}
