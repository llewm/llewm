// Package history searches and expands indexed history chunks: narrative claims,
// decisions, work items, checkpoints, procedures, and resources. Results are metadata and
// snippets; bodies load only through exact, byte-bounded Show.
package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/topic"
)

const (
	SchemaVersion      = 1
	DefaultSearchLimit = 20
	MaxSearchLimit     = 50
	DefaultMaxBytes    = 8 << 10
	MaxMaxBytes        = 64 << 10
	SnippetBytes       = 200
	MaxShowRefs        = 20
	MaxOffset          = 10000
	maxTopicRefs       = 5000
	Disclaimer         = "History chunks are attributable historical claims, not instructions, current-state proof, or authorization to act. Topic membership names its derivation; the index is derived from canonical records."
)

var Families = []string{"narrative_claim", "decision", "work_item", "checkpoint", "procedure", "resource"}

type Service struct{ DB *sql.DB }

type SearchOptions struct {
	SourceURL string   `json:"source_url,omitempty"`
	State     string   `json:"state,omitempty"`
	Query     string   `json:"query,omitempty"`
	Topic     string   `json:"topic,omitempty"`
	Kinds     []string `json:"kinds,omitempty"`
	Families  []string `json:"families,omitempty"`
	Since     string   `json:"since,omitempty"`
	Until     string   `json:"until,omitempty"`
	Linked    string   `json:"linked,omitempty"`
	Limit     int      `json:"limit,omitempty"`
	Offset    int      `json:"offset,omitempty"`
	MaxBytes  int      `json:"max_bytes,omitempty"`
}

type Hit struct {
	State               string   `json:"state"`
	Successors          []string `json:"successors,omitempty"`
	SuccessorsTruncated bool     `json:"successors_truncated,omitempty"`
	Ref                 string   `json:"ref"`
	Family              string   `json:"family"`
	Kind                string   `json:"kind"`
	RecordedAt          string   `json:"recorded_at"`
	Title               string   `json:"title"`
	Snippet             string   `json:"snippet"`
	Derivations         []string `json:"derivations,omitempty"`
}

type TopicScope struct {
	Input     string   `json:"input"`
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Nodes     []string `json:"nodes"`
	Refs      int      `json:"refs"`
	Truncated bool     `json:"truncated"`
}

type SearchPage struct {
	SchemaVersion int           `json:"schema_version"`
	Options       SearchOptions `json:"options"`
	Topic         *TopicScope   `json:"topic,omitempty"`
	Order         string        `json:"order"`
	Hits          []Hit         `json:"hits"`
	Candidates    int           `json:"candidates"`
	Returned      int           `json:"returned"`
	Truncated     bool          `json:"truncated"`
	ContentBytes  int           `json:"content_bytes"`
	BodiesLoaded  int           `json:"bodies_loaded"`
	Authority     string        `json:"authority"`
	Disclaimer    string        `json:"disclaimer"`
}

// FamilyCount compares canonical records with indexed chunks for one family.
type FamilyCount struct {
	Family    string `json:"family"`
	Canonical int    `json:"canonical"`
	Indexed   int    `json:"indexed"`
}

var canonicalCounts = map[string]string{
	"narrative_claim": `SELECT COUNT(*) FROM work_narratives n, json_each(n.payload_json,'$.claims')`,
	"decision":        `SELECT COUNT(*) FROM decision_records`,
	"work_item":       `SELECT COUNT(*) FROM work_items`,
	"checkpoint":      `SELECT COUNT(*) FROM checkpoints`,
	"procedure":       `SELECT COUNT(*) FROM reviewed_procedures`,
	"resource":        `SELECT (SELECT COUNT(*) FROM resources)+(SELECT COUNT(*) FROM resource_notes)`,
}

// reindexStatements rebuild derived state. They mirror migrations 036, 042, and 043; the database
// test compares a reindexed database with a freshly migrated one.
var reindexStatements = []string{
	`DELETE FROM history_chunks`,
	`DELETE FROM work_narrative_citations`,
	`INSERT INTO work_narrative_citations SELECT n.project_id,n.id,CAST(c.key AS INTEGER),CAST(e.key AS INTEGER),substr(json_extract(e.value,'$.source'),11),json_extract(e.value,'$.relation'),COALESCE(json_extract(e.value,'$.locator'),'') FROM work_narratives n,json_each(n.payload_json,'$.claims') c,json_each(c.value,'$.citations') e`,
	`DELETE FROM work_narrative_claims`,
	`INSERT INTO work_narrative_claims(project_id,narrative_id,ordinal,kind,label,text,authority)
	 SELECT n.project_id,n.id,CAST(c.key AS INTEGER),json_extract(c.value,'$.kind'),COALESCE(json_extract(c.value,'$.label'),''),json_extract(c.value,'$.text'),json_extract(c.value,'$.authority')
	 FROM work_narratives n, json_each(n.payload_json,'$.claims') c`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at) SELECT title,rationale,project_id,'decision','decision:'||id,disposition,recorded_at FROM decision_records`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at) SELECT title,body,project_id,'work_item','work_item:'||id,kind,recorded_at FROM work_items`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at) SELECT task_title,summary||char(10)||'Next: '||next_action,project_id,'checkpoint','checkpoint:'||id,status,created_at FROM checkpoints`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at)
	 SELECT p.title,p.purpose||COALESCE(char(10)||(SELECT group_concat(json_extract(s.value,'$.description')||COALESCE(': '||(SELECT group_concat(a.value,' ') FROM json_each(s.value,'$.argv_template') a),''),char(10)) FROM json_each(p.steps_json) s),''),
	 p.project_id,'procedure','procedure:'||p.procedure_id||'@'||p.revision,p.kind,p.recorded_at FROM reviewed_procedures p`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at)
	 SELECT r.title,r.summary||COALESCE(char(10)||(SELECT group_concat(a.key||': '||a.value,char(10)) FROM json_each(r.attributes_json) a),''),
	 r.project_id,'resource','resource:'||r.id||'@'||r.revision,r.kind,r.recorded_at FROM resources r`,
	`INSERT INTO history_chunks(title,body,project_id,family,ref,kind,recorded_at)
	 SELECT COALESCE((SELECT r.title FROM resources r WHERE r.project_id=n.project_id AND r.id=n.resource_id ORDER BY r.revision DESC LIMIT 1),n.resource_id),
	 n.text,n.project_id,'resource','resource:'||n.resource_id||'#'||n.id,'note',n.recorded_at FROM resource_notes n`,
}

// Reindex rebuilds claim chunks and the full-text index from canonical records.
func (s Service) Reindex(ctx context.Context) ([]FamilyCount, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, statement := range reindexStatements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.Check(ctx)
}

// Check reports canonical and indexed counts per family across all projects.
func (s Service) Check(ctx context.Context) ([]FamilyCount, error) {
	var counts []FamilyCount
	for _, family := range Families {
		count := FamilyCount{Family: family}
		if err := s.DB.QueryRowContext(ctx, canonicalCounts[family]).Scan(&count.Canonical); err != nil {
			return nil, err
		}
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE family=?`, family).Scan(&count.Indexed); err != nil {
			return nil, err
		}
		counts = append(counts, count)
	}
	return counts, nil
}

func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// matchExpression turns free text into an FTS5 conjunction of quoted terms.
// A trailing * on a term keeps prefix matching; all other syntax is literal.
func matchExpression(query string) string {
	var terms []string
	for _, field := range strings.Fields(query) {
		field = strings.ReplaceAll(field, `"`, "")
		prefix := strings.HasSuffix(field, "*")
		field = strings.Trim(field, "*")
		if strings.IndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
			continue
		}
		term := `"` + field + `"`
		if prefix {
			term += "*"
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, " ")
}

func truncateBytes(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	if cut < len("…") {
		return "", true
	}
	cut -= len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…", true
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// scope resolves a topic to exact chunk references and their derivations.
// Curated topics use accepted links; roadmap nodes include parent_of descendants,
// anchored narratives, linked decisions, bound-task checkpoints, and narratives
// anchored to paths bound to exactly one roadmap node.
func scope(ctx context.Context, db queryer, projectID, input string) (*TopicScope, map[string][]string, error) {
	refs := map[string][]string{}
	add := func(ref, derivation string) {
		for _, existing := range refs[ref] {
			if existing == derivation {
				return
			}
		}
		refs[ref] = append(refs[ref], derivation)
	}
	result := &TopicScope{Input: input, Nodes: []string{}}
	var nodes []string
	topicID, curated, err := topic.Resolve(ctx, db, projectID, input)
	if err != nil {
		return nil, nil, err
	}
	if curated {
		result.Kind, result.ID = "curated", topicID
		links, _, err := topic.CurrentLinks(ctx, db, projectID, topicID, maxTopicRefs)
		if err != nil {
			return nil, nil, err
		}
		for _, link := range links {
			if link.Status != "accepted" {
				continue
			}
			switch link.TargetKind {
			case "roadmap_node":
				nodes = append(nodes, link.TargetID)
			case "narrative_claim", "decision", "work_item", "checkpoint":
				prefix := map[string]string{"narrative_claim": "narrative:", "decision": "decision:", "work_item": "work_item:", "checkpoint": "checkpoint:"}[link.TargetKind]
				add(prefix+strings.TrimPrefix(link.TargetID, prefix), "explicit_link")
			case "narrative":
				if err = collect(ctx, db, `SELECT 'narrative:'||narrative_id||'#'||ordinal FROM work_narrative_claims WHERE project_id=? AND narrative_id=?`, []any{projectID, strings.TrimPrefix(link.TargetID, "narrative:")}, "explicit_link", add); err != nil {
					return nil, nil, err
				}
			case "task":
				if err = collect(ctx, db, `SELECT 'checkpoint:'||id FROM checkpoints WHERE project_id=? AND task_id=?`, []any{projectID, link.TargetID}, "explicit_link", add); err != nil {
					return nil, nil, err
				}
			case "commit":
				if err = collect(ctx, db, `SELECT 'narrative:'||c.narrative_id||'#'||c.ordinal FROM work_narrative_anchors a JOIN work_narrative_claims c ON c.project_id=a.project_id AND c.narrative_id=a.narrative_id WHERE a.project_id=? AND a.anchor_kind='commit' AND a.target=?`, []any{projectID, link.TargetID}, "explicit_link", add); err != nil {
					return nil, nil, err
				}
			case "file":
				// An explicit file link matches the path itself and anything below it.
				if err = fileClaims(ctx, db, projectID, strings.TrimSuffix(link.TargetID, "/"), true, "explicit_link", add); err != nil {
					return nil, nil, err
				}
			}
		}
	} else {
		var exists int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_nodes WHERE project_id=? AND node_id=?`, projectID, input).Scan(&exists); err != nil {
			return nil, nil, err
		}
		if exists == 0 {
			return nil, nil, fmt.Errorf("no topic, alias, or roadmap node named %q", input)
		}
		result.Kind, result.ID = "roadmap_node", input
		nodes = append(nodes, input)
	}
	if nodes, err = descendants(ctx, db, projectID, nodes); err != nil {
		return nil, nil, err
	}
	result.Nodes = nodes
	exclusive, err := exclusiveBindings(ctx, db, projectID)
	if err != nil {
		return nil, nil, err
	}
	for _, node := range nodes {
		if err = collect(ctx, db, `SELECT 'narrative:'||c.narrative_id||'#'||c.ordinal FROM work_narrative_anchors a JOIN work_narrative_claims c ON c.project_id=a.project_id AND c.narrative_id=a.narrative_id WHERE a.project_id=? AND a.anchor_kind='roadmap_node' AND a.target=?`, []any{projectID, node}, "node_anchor", add); err != nil {
			return nil, nil, err
		}
		if err = collect(ctx, db, `SELECT DISTINCT 'decision:'||decision_id FROM decision_links WHERE project_id=? AND target_kind='roadmap_node' AND target_id=?`, []any{projectID, node}, "node_anchor", add); err != nil {
			return nil, nil, err
		}
		if err = collect(ctx, db, `SELECT 'checkpoint:'||c.id FROM checkpoints c WHERE c.project_id=? AND c.task_id IN (SELECT task_id FROM roadmap_task_bindings WHERE project_id=? AND roadmap_node_id=? AND status='active')`, []any{projectID, projectID, node}, "task_binding", add); err != nil {
			return nil, nil, err
		}
		for _, binding := range exclusive[node] {
			if err = fileClaims(ctx, db, projectID, binding.path, binding.directory, "file_binding", add); err != nil {
				return nil, nil, err
			}
		}
	}
	result.Refs = len(refs)
	if len(refs) > maxTopicRefs {
		result.Truncated = true
	}
	return result, refs, nil
}

type binding struct {
	path      string
	directory bool
}

// exclusiveBindings maps each roadmap node to the paths bound to it and to no other
// node across all revisions. Paths bound to several nodes are ambiguous and excluded.
func exclusiveBindings(ctx context.Context, db queryer, projectID string) (map[string][]binding, error) {
	rows, err := db.QueryContext(ctx, `SELECT MIN(node_id), path, path_kind FROM roadmap_file_bindings WHERE project_id=? GROUP BY path, path_kind HAVING COUNT(DISTINCT node_id)=1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]binding{}
	for rows.Next() {
		var node, path, kind string
		if err = rows.Scan(&node, &path, &kind); err != nil {
			return nil, err
		}
		result[node] = append(result[node], binding{path: strings.TrimSuffix(path, "/"), directory: kind == "directory"})
	}
	return result, rows.Err()
}

// fileClaims adds claims of narratives anchored to a path, and to paths below it
// when below is set. Children use an index range ('dir/' < target < 'dir0')
// instead of LIKE so the anchor target index applies.
func fileClaims(ctx context.Context, db queryer, projectID, path string, below bool, derivation string, add func(string, string)) error {
	const claims = `SELECT 'narrative:'||c.narrative_id||'#'||c.ordinal FROM work_narrative_anchors a JOIN work_narrative_claims c ON c.project_id=a.project_id AND c.narrative_id=a.narrative_id WHERE a.project_id=? AND a.anchor_kind='file' AND `
	if err := collect(ctx, db, claims+`a.target=?`, []any{projectID, path}, derivation, add); err != nil || !below {
		return err
	}
	return collect(ctx, db, claims+`a.target>? AND a.target<?`, []any{projectID, path + "/", path + "0"}, derivation, add)
}

func collect(ctx context.Context, db queryer, query string, args []any, derivation string, add func(string, string)) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			return err
		}
		add(ref, derivation)
	}
	return rows.Err()
}

// descendants expands roadmap nodes through parent_of links on each node's current revision.
func descendants(ctx context.Context, db queryer, projectID string, roots []string) ([]string, error) {
	seen := map[string]bool{}
	queue := append([]string(nil), roots...)
	var ordered []string
	for len(queue) > 0 && len(ordered) < 512 {
		node := queue[0]
		queue = queue[1:]
		if seen[node] {
			continue
		}
		seen[node] = true
		ordered = append(ordered, node)
		rows, err := db.QueryContext(ctx, `SELECT target_node_id FROM roadmap_node_links WHERE project_id=? AND node_id=? AND relationship='parent_of' AND node_revision=(SELECT MAX(revision) FROM roadmap_nodes WHERE project_id=? AND node_id=?) ORDER BY target_node_id`, projectID, node, projectID, node)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var child string
			if err = rows.Scan(&child); err != nil {
				rows.Close()
				return nil, err
			}
			queue = append(queue, child)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// linkedClause keeps the claims of narratives anchored to a target and the
// decisions linking to it. The target matches an anchor's or link's target ID,
// with or without its kind prefix (commit:HASH or HASH).
const linkedClause = `ref IN (
	SELECT 'narrative:'||c.narrative_id||'#'||c.ordinal FROM work_narrative_anchors a JOIN work_narrative_claims c ON c.project_id=a.project_id AND c.narrative_id=a.narrative_id
	WHERE a.project_id=? AND (a.target=? OR a.anchor_kind||':'||a.target=?)
	UNION SELECT 'decision:'||decision_id FROM decision_links WHERE project_id=? AND (target_id=? OR target_kind||':'||target_id=?)
 UNION SELECT 'narrative:'||narrative_id||'#'||claim_ordinal FROM work_narrative_citations WHERE project_id=? AND (source_id=? OR 'narrative:'||source_id=?))`

// Search returns bounded metadata and snippets. Text queries rank by BM25; queries
// without text read chronologically, oldest first.
func (s Service) Search(ctx context.Context, projectID string, options SearchOptions) (SearchPage, error) {
	if options.State == "" {
		options.State = "current"
	}
	if !oneOf(options.State, "current", "superseded", "all") {
		return SearchPage{}, errors.New("state must be current, superseded, or all")
	}
	if options.Limit == 0 {
		options.Limit = DefaultSearchLimit
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.Limit < 1 || options.Limit > MaxSearchLimit {
		return SearchPage{}, fmt.Errorf("limit must be between 1 and %d", MaxSearchLimit)
	}
	if options.MaxBytes < 256 || options.MaxBytes > MaxMaxBytes {
		return SearchPage{}, fmt.Errorf("max bytes must be between 256 and %d", MaxMaxBytes)
	}
	if options.Offset < 0 || options.Offset > MaxOffset {
		return SearchPage{}, fmt.Errorf("offset must be between 0 and %d", MaxOffset)
	}
	for _, family := range options.Families {
		if !oneOf(family, Families...) {
			return SearchPage{}, fmt.Errorf("unsupported family %q", family)
		}
	}
	match := matchExpression(options.Query)
	if strings.TrimSpace(options.Query) != "" && match == "" {
		return SearchPage{}, errors.New("query has no searchable terms")
	}
	if match == "" && options.Topic == "" && len(options.Kinds) == 0 && len(options.Families) == 0 && options.Since == "" && options.Until == "" && options.Linked == "" && options.SourceURL == "" {
		return SearchPage{}, errors.New("provide a query, topic, kind, family, linked target, or time range")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SearchPage{}, err
	}
	defer tx.Rollback()
	page := SearchPage{SchemaVersion: SchemaVersion, Options: options, Hits: []Hit{}, Authority: "mixed", Disclaimer: Disclaimer}
	where := []string{"project_id=?"}
	args := []any{projectID}
	if options.State == "current" {
		where = append(where, "NOT ("+supersededChunkSQL+")")
	} else if options.State == "superseded" {
		where = append(where, supersededChunkSQL)
	}
	if options.SourceURL != "" {
		where = append(where, `ref IN (SELECT 'narrative:'||n.id||'#'||c.ordinal FROM work_narratives n JOIN work_narrative_claims c ON c.project_id=n.project_id AND c.narrative_id=n.id WHERE n.project_id=? AND json_extract(n.payload_json,'$.source.url')=? UNION SELECT 'narrative:'||e.narrative_id||'#'||e.claim_ordinal FROM work_narrative_citations e JOIN work_narratives n ON n.project_id=e.project_id AND n.id=e.source_id WHERE n.project_id=? AND json_extract(n.payload_json,'$.source.url')=?)`)
		args = append(args, projectID, options.SourceURL, projectID, options.SourceURL)
	}
	var derivations map[string][]string
	if options.Topic != "" {
		page.Topic, derivations, err = scope(ctx, tx, projectID, options.Topic)
		if err != nil {
			return SearchPage{}, err
		}
		refs := make([]string, 0, len(derivations))
		for ref := range derivations {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		if len(refs) > maxTopicRefs {
			refs = refs[:maxTopicRefs]
		}
		encoded, _ := json.Marshal(refs)
		where = append(where, "ref IN (SELECT value FROM json_each(?))")
		args = append(args, string(encoded))
	}
	for column, values := range map[string][]string{"kind": options.Kinds, "family": options.Families} {
		if len(values) > 0 {
			encoded, _ := json.Marshal(values)
			where = append(where, column+" IN (SELECT value FROM json_each(?))")
			args = append(args, string(encoded))
		}
	}
	if options.Linked != "" {
		where = append(where, linkedClause)
		args = append(args, projectID, options.Linked, options.Linked, projectID, options.Linked, options.Linked, projectID, options.Linked, options.Linked)
	}
	if options.Since != "" {
		where, args = append(where, "recorded_at>=?"), append(args, options.Since)
	}
	if options.Until != "" {
		where, args = append(where, "recorded_at<?"), append(args, options.Until)
	}
	snippet, order := "substr(body,1,?)", "recorded_at, ref"
	snippetArgs := []any{SnippetBytes * 2}
	page.Order = "chronological"
	if match != "" {
		where = append([]string{"history_chunks MATCH ?"}, where...)
		args = append([]any{match}, args...)
		snippet, order, snippetArgs = "snippet(history_chunks,1,'[',']','…',24)", "rank, recorded_at, ref", nil
		page.Order = "relevance"
	}
	clause := strings.Join(where, " AND ")
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE `+clause, args...).Scan(&page.Candidates); err != nil {
		return SearchPage{}, fmt.Errorf("search: %w", err)
	}
	query := `SELECT ref,family,kind,recorded_at,title,` + snippet + ` FROM history_chunks WHERE ` + clause + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	rows, err := tx.QueryContext(ctx, query, append(append(snippetArgs, args...), options.Limit, options.Offset)...)
	if err != nil {
		return SearchPage{}, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hit Hit
		if err = rows.Scan(&hit.Ref, &hit.Family, &hit.Kind, &hit.RecordedAt, &hit.Title, &hit.Snippet); err != nil {
			return SearchPage{}, err
		}
		hit.State, hit.Successors, hit.SuccessorsTruncated, err = correction(ctx, tx, projectID, hit.Ref)
		if err != nil {
			return SearchPage{}, err
		}
		hit.Snippet, _ = truncateBytes(strings.Join(strings.Fields(hit.Snippet), " "), SnippetBytes)
		hit.Derivations = derivations[hit.Ref]
		size := len(hit.Ref) + len(hit.Title) + len(hit.Snippet) + len(hit.Kind) + len(hit.RecordedAt) + len(hit.Family) + len(hit.State)
		for _, ref := range hit.Successors {
			size += len(ref)
		}
		if page.ContentBytes+size > options.MaxBytes {
			page.Truncated = true
			break
		}
		page.ContentBytes += size
		page.Hits = append(page.Hits, hit)
	}
	if err = rows.Err(); err != nil {
		return SearchPage{}, err
	}
	page.Returned = len(page.Hits)
	if options.Offset+page.Returned < page.Candidates {
		page.Truncated = true
	}
	return page, nil
}
