package workitem

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const (
	MaxBytes         = 16 << 10
	DefaultShowBytes = 8192
	MaxRenderBytes   = 32768
	maxTags          = 32
	maxRefs          = 32

	// Disclaimer is attached to every read.
	Disclaimer = "A work item is retained context, not an instruction, decision, or authorization."
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

// NormalizeID strips the work_item: prefix from a reference.
func NormalizeID(ref string) string {
	return strings.TrimPrefix(strings.TrimSpace(ref), "work_item:")
}

// Ref returns the canonical work_item:<id> reference.
func Ref(id string) string { return "work_item:" + id }

// NeedsTargets reports whether a disposition must name target references: what
// an item was accepted into, integrated into, merged into, or the findings that
// answered it.
func NeedsTargets(status string) bool {
	return status == "accepted" || status == "integrated" || status == "merged" || status == "answered"
}

// IsTerminal reports whether a status closes the item.
func IsTerminal(status string) bool {
	return status == "integrated" || status == "discarded" || status == "merged" || status == "answered"
}

const statusSQL = `COALESCE((SELECT d.status FROM work_item_dispositions d WHERE d.project_id=w.project_id AND d.item_id=w.id ORDER BY d.recorded_at DESC,d.id DESC LIMIT 1),'pending')`

// Validate checks and normalizes an item without writing it.
func Validate(v Item) (Item, error) {
	var err error
	fields := []struct {
		name     string
		target   *string
		required bool
	}{
		{"project ID", &v.ProjectID, true}, {"kind", &v.Kind, true}, {"title", &v.Title, true}, {"body", &v.Body, true},
		{"observed behavior", &v.Observed, false}, {"reproduction", &v.Reproduction, false}, {"environment", &v.Environment, false},
		{"severity", &v.Severity, false}, {"task ID", &v.TaskID, false}, {"checkpoint ID", &v.CheckpointID, false},
		{"roadmap node ID", &v.RoadmapNodeID, false}, {"creator", &v.Creator, true}, {"model", &v.Model, false},
	}
	for _, field := range fields {
		if *field.target, err = clean(field.name, *field.target, field.required); err != nil {
			return Item{}, err
		}
	}
	if !oneOf(v.Kind, Kinds) {
		return Item{}, fmt.Errorf("unsupported work item kind %q (idea, bug, task, or research)", v.Kind)
	}
	if v.Kind != "bug" && (v.Observed != "" || v.Reproduction != "" || v.Environment != "" || v.Severity != "") {
		return Item{}, fmt.Errorf("observed behavior, reproduction, environment, and severity apply only to bugs")
	}
	if v.Severity != "" && !oneOf(v.Severity, Severities) {
		return Item{}, fmt.Errorf("unsupported severity %q (low, medium, high, or critical)", v.Severity)
	}
	if v.ID == "" {
		v.ID = activity.NewID()
	}
	if v.ID, err = clean("work item ID", NormalizeID(v.ID), true); err != nil {
		return Item{}, err
	}
	if len(v.Tags) > maxTags {
		return Item{}, fmt.Errorf("work items accept at most %d tags", maxTags)
	}
	tags := make([]string, 0, len(v.Tags))
	seen := map[string]bool{}
	for _, tag := range v.Tags {
		if tag, err = clean("tag", tag, true); err != nil {
			return Item{}, err
		}
		if seen[tag] {
			return Item{}, fmt.Errorf("duplicate tag %q", tag)
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	v.Tags = tags
	if v.Authority == "" {
		v.Authority = "caller_asserted"
	}
	if !oneOf(v.Authority, []string{"caller_asserted", "user_reported", "model_suggested"}) {
		return Item{}, fmt.Errorf("unsupported work item authority %q", v.Authority)
	}
	v.SchemaVersion = SchemaVersion
	return v, nil
}

// Capture appends one pending work item with its links. A From promotion also
// records the source item as integrated into this one.
func (s Service) Capture(ctx context.Context, request Capture) (Item, error) {
	v, err := Validate(request.Item)
	if err != nil {
		return Item{}, err
	}
	v.Status = "pending"
	v.RecordedAt = s.now()
	links := []Link{}
	from := strings.TrimSpace(request.From)
	var promoted string
	switch {
	case from == "":
	case strings.HasPrefix(from, "work_item:"):
		promoted = NormalizeID(from)
		links = append(links, Link{Relationship: "derived_from", TargetRef: Ref(promoted)})
	case strings.HasPrefix(from, "friction:"):
		links = append(links, Link{Relationship: "derived_from", TargetRef: from})
	default:
		return Item{}, fmt.Errorf("from must be a work_item:<id> or friction:<id> reference")
	}
	links = append(links, request.Links...)
	if len(links) > maxRefs {
		return Item{}, fmt.Errorf("work items accept at most %d links", maxRefs)
	}
	for i := range links {
		link := &links[i]
		if !oneOf(link.Relationship, Relationships) {
			return Item{}, fmt.Errorf("unsupported work item relationship %q", link.Relationship)
		}
		if link.TargetRef, err = clean("link target", link.TargetRef, true); err != nil {
			return Item{}, err
		}
		link.ID = activity.NewID()
		link.ProjectID, link.ItemID, link.Creator = v.ProjectID, v.ID, v.Creator
		link.Authority, link.RecordedAt, link.SchemaVersion = "caller_asserted", v.RecordedAt, SchemaVersion
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	if promoted != "" {
		status, err := currentStatus(ctx, tx, v.ProjectID, promoted)
		if err != nil {
			return Item{}, err
		}
		if IsTerminal(status) {
			return Item{}, fmt.Errorf("work item %s is already %s", promoted, status)
		}
	}
	if err := Insert(ctx, tx, v); err != nil {
		return Item{}, err
	}
	for _, link := range links {
		if err := InsertLink(ctx, tx, link); err != nil {
			return Item{}, err
		}
	}
	v.Links = links
	if promoted != "" {
		d := Disposition{ID: activity.NewID(), ProjectID: v.ProjectID, ItemID: promoted, Status: "integrated", Reason: fmt.Sprintf("Promoted to %s %s", v.Kind, Ref(v.ID)), TargetRefs: []string{Ref(v.ID)}, Creator: v.Creator, Model: v.Model, Authority: "caller_asserted", RecordedAt: v.RecordedAt, SchemaVersion: SchemaVersion}
		if err := InsertDisposition(ctx, tx, d); err != nil {
			return Item{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	return v, nil
}

// Insert writes one validated item and its tags.
func Insert(ctx context.Context, tx *sql.Tx, v Item) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO work_items(id,project_id,kind,title,body,observed,reproduction,environment,severity,task_id,checkpoint_id,roadmap_node_id,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,NULLIF(?,''),?,?,?)`,
		v.ID, v.ProjectID, v.Kind, v.Title, v.Body, v.Observed, v.Reproduction, v.Environment, v.Severity, v.TaskID, v.CheckpointID, v.RoadmapNodeID, v.Creator, v.Model, v.Authority, v.RecordedAt.Format(time.RFC3339Nano), v.SchemaVersion)
	if err != nil {
		return fmt.Errorf("append work item %s: %w", v.ID, err)
	}
	for _, tag := range v.Tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_item_tags(project_id,item_id,tag) VALUES(?,?,?)`, v.ProjectID, v.ID, tag); err != nil {
			return fmt.Errorf("append work item tag: %w", err)
		}
	}
	return nil
}

// InsertLink writes one link.
func InsertLink(ctx context.Context, tx *sql.Tx, l Link) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO work_item_links(id,project_id,item_id,relationship,target_ref,creator,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?)`,
		l.ID, l.ProjectID, l.ItemID, l.Relationship, l.TargetRef, l.Creator, l.Authority, l.RecordedAt.Format(time.RFC3339Nano), l.SchemaVersion)
	if err != nil {
		return fmt.Errorf("append work item link %s: %w", l.ID, err)
	}
	return nil
}

// InsertDisposition writes one disposition.
func InsertDisposition(ctx context.Context, tx *sql.Tx, d Disposition) error {
	refs, _ := json.Marshal(d.TargetRefs)
	_, err := tx.ExecContext(ctx, `INSERT INTO work_item_dispositions(id,project_id,item_id,status,reason,target_refs_json,creator,model,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,NULLIF(?,''),?,?,?)`,
		d.ID, d.ProjectID, d.ItemID, d.Status, d.Reason, string(refs), d.Creator, d.Model, d.Authority, d.RecordedAt.Format(time.RFC3339Nano), d.SchemaVersion)
	if err != nil {
		return fmt.Errorf("append work item disposition %s: %w", d.ID, err)
	}
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func currentStatus(ctx context.Context, q queryer, projectID, id string) (string, error) {
	var status string
	err := q.QueryRowContext(ctx, `SELECT `+statusSQL+` FROM work_items w WHERE w.project_id=? AND w.id=?`, projectID, id).Scan(&status)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("work item %q not found in this project", id)
	}
	return status, err
}

// Dispose appends one reviewed disposition. Terminal items cannot change, an
// accepted item cannot be accepted again, and accepted, integrated, and merged
// need a target reference.
func (s Service) Dispose(ctx context.Context, d Disposition) (Disposition, error) {
	var err error
	if d.ProjectID, err = clean("project ID", d.ProjectID, true); err != nil {
		return Disposition{}, err
	}
	d.ItemID = NormalizeID(d.ItemID)
	if d.Reason, err = clean("reason", d.Reason, true); err != nil {
		return Disposition{}, err
	}
	if d.Creator, err = clean("creator", d.Creator, true); err != nil {
		return Disposition{}, err
	}
	if d.Model, err = clean("model", d.Model, false); err != nil {
		return Disposition{}, err
	}
	if !oneOf(d.Status, Statuses) {
		return Disposition{}, fmt.Errorf("unsupported work item disposition %q", d.Status)
	}
	if len(d.TargetRefs) > maxRefs {
		return Disposition{}, fmt.Errorf("disposition accepts at most %d target references", maxRefs)
	}
	refs := make([]string, 0, len(d.TargetRefs))
	for _, ref := range d.TargetRefs {
		if ref, err = clean("target reference", ref, true); err != nil {
			return Disposition{}, err
		}
		refs = append(refs, ref)
	}
	d.TargetRefs = refs
	if NeedsTargets(d.Status) && len(d.TargetRefs) == 0 {
		return Disposition{}, fmt.Errorf("%s disposition requires at least one target reference", d.Status)
	}
	d.ID = activity.NewID()
	d.Authority = "caller_asserted"
	d.RecordedAt = s.now()
	d.SchemaVersion = SchemaVersion
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Disposition{}, err
	}
	defer tx.Rollback()
	current, err := currentStatus(ctx, tx, d.ProjectID, d.ItemID)
	if err != nil {
		return Disposition{}, err
	}
	if IsTerminal(current) {
		return Disposition{}, fmt.Errorf("work item already has terminal disposition %q", current)
	}
	if d.Status == "answered" {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE project_id=? AND id=?`, d.ProjectID, d.ItemID).Scan(&kind); err != nil {
			return Disposition{}, err
		}
		if kind != "research" {
			return Disposition{}, fmt.Errorf("only a research item can be answered; this one is a %s", kind)
		}
		if err := ValidateAnswerTargets(ctx, tx, d.ProjectID, d.TargetRefs); err != nil {
			return Disposition{}, err
		}
	}
	if current == "accepted" && d.Status == "accepted" {
		return Disposition{}, fmt.Errorf("work item is already accepted")
	}
	if err := InsertDisposition(ctx, tx, d); err != nil {
		return Disposition{}, err
	}
	if err := tx.Commit(); err != nil {
		return Disposition{}, err
	}
	return d, nil
}

// ListPage returns bounded summaries in recording order. Status "open" matches
// pending, accepted, and deferred items.
func (s Service) ListPage(ctx context.Context, projectID string, o ListOptions) (ListPage, error) {
	if o.Limit == 0 {
		o.Limit = 20
	}
	if o.Limit < 1 || o.Limit > 100 {
		return ListPage{}, fmt.Errorf("limit must be between 1 and 100")
	}
	if o.Status != "" && o.Status != "pending" && o.Status != "open" && !oneOf(o.Status, Statuses) {
		return ListPage{}, fmt.Errorf("unsupported work item status %q", o.Status)
	}
	if o.Kind != "" && !oneOf(o.Kind, Kinds) {
		return ListPage{}, fmt.Errorf("unsupported work item kind %q", o.Kind)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,kind,title,status,severity,task_id,roadmap_node_id,recorded_at,COUNT(*) OVER() FROM (
		SELECT w.id,w.kind,w.title,`+statusSQL+` status,COALESCE(w.severity,'') severity,COALESCE(w.task_id,'') task_id,COALESCE(w.roadmap_node_id,'') roadmap_node_id,w.recorded_at
		FROM work_items w WHERE w.project_id=?1 AND (?2='' OR w.kind=?2) AND (?3='' OR w.task_id=?3) AND (?4='' OR w.roadmap_node_id=?4)
		AND (?5='' OR EXISTS(SELECT 1 FROM work_item_tags t WHERE t.project_id=w.project_id AND t.item_id=w.id AND t.tag=?5)))
		WHERE ?6='' OR status=?6 OR (?6='open' AND status IN ('pending','accepted','deferred'))
		ORDER BY recorded_at,id LIMIT ?7`, projectID, o.Kind, o.TaskID, o.RoadmapNodeID, o.Tag, o.Status, o.Limit)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Items: []Summary{}, Limit: o.Limit}
	for rows.Next() {
		var x Summary
		var at string
		if err = rows.Scan(&x.ID, &x.Kind, &x.Title, &x.Status, &x.Severity, &x.TaskID, &x.RoadmapNodeID, &at, &page.Total); err != nil {
			rows.Close()
			return ListPage{}, err
		}
		if x.RecordedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			rows.Close()
			return ListPage{}, err
		}
		page.Items = append(page.Items, x)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return ListPage{}, err
	}
	for i := range page.Items {
		if page.Items[i].Tags, err = tags(ctx, s.DB, projectID, page.Items[i].ID); err != nil {
			return ListPage{}, err
		}
	}
	page.Truncated = page.Total > len(page.Items)
	return page, nil
}

// Counts returns open items by kind.
func (s Service) Counts(ctx context.Context, projectID string) (Counts, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT w.kind,COUNT(*) FROM work_items w WHERE w.project_id=? AND `+statusSQL+` IN ('pending','accepted','deferred') GROUP BY w.kind`, projectID)
	if err != nil {
		return Counts{}, err
	}
	defer rows.Close()
	counts := Counts{ByKind: map[string]int{}}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return Counts{}, err
		}
		counts.ByKind[kind] = n
		counts.Open += n
	}
	return counts, rows.Err()
}

// Notice is the bootstrap line for open work items, or "" when none are open.
func Notice(counts Counts) string {
	if counts.Open == 0 {
		return ""
	}
	parts := []string{}
	for _, kind := range Kinds {
		if n := counts.ByKind[kind]; n > 0 {
			label := kind + "s"
			switch {
			case kind == "research":
				label = "open questions"
				if n == 1 {
					label = "open question"
				}
			case n == 1:
				label = kind
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	return fmt.Sprintf("%d open (%s); list with llewm item list --status open.", counts.Open, strings.Join(parts, ", "))
}

func tags(ctx context.Context, q queryer, projectID, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT tag FROM work_item_tags WHERE project_id=? AND item_id=? ORDER BY tag`, projectID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		values = append(values, tag)
	}
	return values, rows.Err()
}

// Get loads one item with its tags, links, and dispositions; nil when absent.
func (s Service) Get(ctx context.Context, projectID, id string) (*Item, error) {
	id = NormalizeID(id)
	var v Item
	var at string
	err := s.DB.QueryRowContext(ctx, `SELECT w.id,w.project_id,w.kind,w.title,w.body,COALESCE(w.observed,''),COALESCE(w.reproduction,''),COALESCE(w.environment,''),COALESCE(w.severity,''),COALESCE(w.task_id,''),COALESCE(w.checkpoint_id,''),COALESCE(w.roadmap_node_id,''),w.creator,COALESCE(w.model,''),w.authority,w.recorded_at,w.schema_version,`+statusSQL+` FROM work_items w WHERE w.project_id=? AND w.id=?`, projectID, id).Scan(
		&v.ID, &v.ProjectID, &v.Kind, &v.Title, &v.Body, &v.Observed, &v.Reproduction, &v.Environment, &v.Severity, &v.TaskID, &v.CheckpointID, &v.RoadmapNodeID, &v.Creator, &v.Model, &v.Authority, &at, &v.SchemaVersion, &v.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.RecordedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return nil, err
	}
	if v.Tags, err = tags(ctx, s.DB, projectID, id); err != nil {
		return nil, err
	}
	if v.Links, err = s.links(ctx, projectID, id); err != nil {
		return nil, err
	}
	if v.Dispositions, err = s.dispositions(ctx, projectID, id); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s Service) links(ctx context.Context, projectID, id string) ([]Link, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,relationship,target_ref,creator,authority,recorded_at,schema_version FROM work_item_links WHERE project_id=? AND item_id=? ORDER BY recorded_at,id`, projectID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Link{}
	for rows.Next() {
		value := Link{ProjectID: projectID, ItemID: id}
		var at string
		if err := rows.Scan(&value.ID, &value.Relationship, &value.TargetRef, &value.Creator, &value.Authority, &at, &value.SchemaVersion); err != nil {
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s Service) dispositions(ctx context.Context, projectID, id string) ([]Disposition, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,status,reason,target_refs_json,creator,COALESCE(model,''),authority,recorded_at,schema_version FROM work_item_dispositions WHERE project_id=? AND item_id=? ORDER BY recorded_at,id`, projectID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Disposition{}
	for rows.Next() {
		value := Disposition{ProjectID: projectID, ItemID: id}
		var refs, at string
		if err := rows.Scan(&value.ID, &value.Status, &value.Reason, &refs, &value.Creator, &value.Model, &value.Authority, &at, &value.SchemaVersion); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(refs), &value.TargetRefs); err != nil {
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// Show renders one item as bounded Markdown; nil when absent.
func (s Service) Show(ctx context.Context, projectID, id string, maxBytes int) (*ShowResult, error) {
	if maxBytes == 0 {
		maxBytes = DefaultShowBytes
	}
	if maxBytes < 512 || maxBytes > MaxRenderBytes {
		return nil, fmt.Errorf("max bytes must be between 512 and %d", MaxRenderBytes)
	}
	v, err := s.Get(ctx, projectID, id)
	if err != nil || v == nil {
		return nil, err
	}
	markdown, truncated := bound(renderMarkdown(*v), maxBytes)
	rendered := len(markdown)
	return &ShowResult{
		SchemaVersion: SchemaVersion, ID: v.ID, Item: *v,
		Metadata: Summary{ID: v.ID, Kind: v.Kind, Title: v.Title, Status: v.Status, Severity: v.Severity, Tags: v.Tags, TaskID: v.TaskID, RoadmapNodeID: v.RoadmapNodeID, RecordedAt: v.RecordedAt},
		Markdown: markdown, MaxBytes: maxBytes, RenderedBytes: rendered, EstimatedTokens: (rendered + 3) / 4,
		Truncated: truncated, Authority: v.Authority, Disclaimer: Disclaimer,
	}, nil
}

func renderMarkdown(v Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- Reference: `%s`\n- Kind: %s\n- Status: %s\n- Recorded: %s\n- Creator: %s\n", v.Title, Ref(v.ID), v.Kind, v.Status, v.RecordedAt.Format(time.RFC3339Nano), v.Creator)
	for _, field := range []struct{ label, value string }{
		{"Model", v.Model}, {"Severity", v.Severity}, {"Originating task", v.TaskID},
		{"Originating checkpoint", v.CheckpointID}, {"Roadmap node", v.RoadmapNodeID},
	} {
		if field.value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", field.label, field.value)
		}
	}
	if len(v.Tags) > 0 {
		fmt.Fprintf(&b, "- Tags: %s\n", strings.Join(v.Tags, ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", v.Body)
	for _, section := range []struct{ heading, value string }{
		{"Observed behavior", v.Observed}, {"Reproduction", v.Reproduction}, {"Environment", v.Environment},
	} {
		if section.value != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", section.heading, section.value)
		}
	}
	if len(v.Links) > 0 {
		b.WriteString("\n## Links\n\n")
		for _, link := range v.Links {
			fmt.Fprintf(&b, "- %s `%s`\n", link.Relationship, link.TargetRef)
		}
	}
	if len(v.Dispositions) > 0 {
		b.WriteString("\n## Disposition history\n")
		for _, d := range v.Dispositions {
			fmt.Fprintf(&b, "\n### %s\n\n- Recorded: %s\n- Reviewer: %s\n- Reason: %s\n", d.Status, d.RecordedAt.Format(time.RFC3339Nano), d.Creator, d.Reason)
			if len(d.TargetRefs) > 0 {
				fmt.Fprintf(&b, "- Targets: %s\n", strings.Join(d.TargetRefs, ", "))
			}
		}
	}
	b.WriteString("\n> " + Disclaimer + "\n")
	return b.String()
}

func bound(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	marker := "\n\n[Work item truncated at the requested byte budget.]\n"
	if len(marker) >= maxBytes {
		return marker[:maxBytes], true
	}
	body := []byte(value)[:maxBytes-len(marker)]
	for !utf8.Valid(body) {
		body = body[:len(body)-1]
	}
	return strings.TrimRight(string(body), "\n") + marker, true
}

// Audit builds a bounded packet of pending items with bodies, for review.
func (s Service) Audit(ctx context.Context, projectID, kind string, maxBytes int) (Audit, error) {
	if maxBytes == 0 {
		maxBytes = 8192
	}
	if maxBytes < 512 || maxBytes > MaxRenderBytes {
		return Audit{}, fmt.Errorf("max bytes must be between 512 and %d", MaxRenderBytes)
	}
	page, err := s.ListPage(ctx, projectID, ListOptions{Kind: kind, Status: "pending", Limit: 100})
	if err != nil {
		return Audit{}, err
	}
	var b strings.Builder
	b.WriteString("# Pending work items\n\nRetrieval does not promote or authorize an item. Record dispositions only after explicit review.\n\n")
	included := 0
	truncated := page.Truncated
	for _, x := range page.Items {
		v, err := s.Get(ctx, projectID, x.ID)
		if err != nil {
			return Audit{}, err
		}
		section := fmt.Sprintf("## %s\n\n- Reference: `%s`\n- Kind: %s\n- Recorded: %s\n- Tags: %s\n\n%s\n\n", v.Title, Ref(v.ID), v.Kind, v.RecordedAt.Format(time.RFC3339Nano), strings.Join(v.Tags, ", "), v.Body)
		if b.Len()+len(section) > maxBytes {
			truncated = true
			break
		}
		b.WriteString(section)
		included++
	}
	if truncated {
		marker := "_Additional pending items omitted by byte budget._\n"
		if b.Len()+len(marker) <= maxBytes {
			b.WriteString(marker)
		}
	}
	return Audit{Markdown: b.String(), PendingCount: page.Total, IncludedCount: included, RenderedBytes: b.Len(), Truncated: truncated, Authority: "caller_asserted", Disclaimer: "Read-only pending work-item packet; discussion does not change dispositions or roadmap state."}, nil
}
