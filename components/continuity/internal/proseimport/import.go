// Package proseimport validates agent-interpreted project prose. It does not
// infer plans, call a model, edit source docs or grant planning authority.
package proseimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/redact"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

const MaxBundleBytes = 512 << 10
const MaxSourceBytes = 64 << 10
const MaxTotalBytes = 256 << 10

type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Document struct {
	Source
	Text string `json:"text"`
}
type Track struct {
	ID                   string           `json:"id"`
	PlanID               string           `json:"plan_id"`
	Title                string           `json:"title"`
	Summary              string           `json:"summary"`
	SourcePath           string           `json:"source_path"`
	Section              string           `json:"section"`
	ExpectedNodeRevision int              `json:"expected_node_revision"`
	ExpectedPlanRevision int              `json:"expected_plan_revision"`
	Items                []trackplan.Item `json:"items"`
}
type Bundle struct {
	Version int      `json:"version"`
	ID      string   `json:"id"`
	Creator string   `json:"creator"`
	Model   string   `json:"model"`
	Sources []Source `json:"sources"`
	Tracks  []Track  `json:"tracks"`
}
type Imported struct {
	NodeID       string `json:"node_id"`
	NodeRevision int    `json:"node_revision"`
	PlanID       string `json:"plan_id"`
	PlanRevision int    `json:"plan_revision"`
	PlanStatus   string `json:"plan_status"`
}
type Receipt struct {
	ID           string     `json:"id"`
	BundleSHA256 string     `json:"bundle_sha256"`
	Sources      []Source   `json:"sources"`
	Tracks       []Imported `json:"tracks"`
	Creator      string     `json:"creator"`
	Model        string     `json:"model"`
	RecordedAt   time.Time  `json:"recorded_at"`
	State        string     `json:"state"`
}
type Service struct {
	DB        *sql.DB
	Root      string
	ProjectID string
	Plans     trackplan.Service
}

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// Snapshot reads only explicitly selected prose, bounded before redaction.
// Source hashes identify exact original bytes; only redacted text is returned.
func Snapshot(root string, paths []string, budget int) ([]Document, error) {
	if len(paths) == 0 || len(paths) > 16 || budget < 1 || budget > MaxTotalBytes {
		return nil, fmt.Errorf("select 1..16 prose files and a byte budget within 1..%d", MaxTotalBytes)
	}
	seen := map[string]bool{}
	var result []Document
	for _, path := range paths {
		path = filepath.ToSlash(filepath.Clean(path))
		if seen[path] {
			return nil, fmt.Errorf("duplicate source %q", path)
		}
		seen[path] = true
		switch strings.ToLower(filepath.Ext(path)) {
		case ".md", ".txt", ".rst":
		default:
			return nil, fmt.Errorf("source %q must be Markdown, text or reStructuredText", path)
		}
		full, err := adoption.SafePath(root, path)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > MaxSourceBytes || len(b) > budget {
			return nil, fmt.Errorf("source %q exceeds the file or remaining total byte budget; select a narrower set", path)
		}
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return nil, fmt.Errorf("source %q is not UTF-8 prose", path)
		}
		budget -= len(b)
		result = append(result, Document{Source: Source{Path: path, SHA256: digest(b)}, Text: redact.String(string(b))})
	}
	return result, nil
}

// Decode rejects typos and excess data instead of silently discarding fields.
func Decode(b []byte) (Bundle, error) {
	if len(b) > MaxBundleBytes {
		return Bundle{}, fmt.Errorf("import bundle exceeds %d bytes", MaxBundleBytes)
	}
	var value Bundle
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return value, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return value, fmt.Errorf("import bundle must contain one JSON object")
	}
	return value, nil
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func normalize(value Bundle) (Bundle, string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return value, "", err
	}
	// Redact before either semantic persistence or receipt/hash persistence.
	var raw any
	if err = json.Unmarshal(b, &raw); err != nil {
		return value, "", err
	}
	b, err = json.Marshal(redact.Value(raw))
	if err != nil {
		return value, "", err
	}
	value, err = Decode(b)
	if err != nil {
		return value, "", err
	}
	if value.Version != 1 || !identifier.MatchString(value.ID) || strings.TrimSpace(value.Creator) == "" || strings.TrimSpace(value.Model) == "" {
		return value, "", fmt.Errorf("version 1, stable import id, creator and model are required")
	}
	if len(value.Sources) == 0 || len(value.Sources) > 16 || len(value.Tracks) == 0 || len(value.Tracks) > 32 {
		return value, "", fmt.Errorf("bundle requires 1..16 sources and 1..32 tracks")
	}
	sources := map[string]bool{}
	for _, source := range value.Sources {
		if source.Path != filepath.ToSlash(filepath.Clean(source.Path)) || sources[source.Path] || !hashPattern.MatchString(source.SHA256) {
			return value, "", fmt.Errorf("invalid or duplicate source %q", source.Path)
		}
		sources[source.Path] = true
	}
	nodes, plans := map[string]bool{}, map[string]bool{}
	for _, t := range value.Tracks {
		if !identifier.MatchString(t.ID) || !identifier.MatchString(t.PlanID) || nodes[t.ID] || plans[t.PlanID] || !sources[t.SourcePath] || strings.TrimSpace(t.Section) == "" || len(t.Section) > 256 || t.ExpectedNodeRevision < 0 || t.ExpectedPlanRevision < 0 {
			return value, "", fmt.Errorf("track %q requires unique stable node/plan IDs, source section and nonnegative expected revisions", t.ID)
		}
		nodes[t.ID], plans[t.PlanID] = true, true
	}
	return value, digest(b), nil
}

func (s Service) checkSources(sources []Source, tracks []Track) error {
	paths := make([]string, len(sources))
	for i, v := range sources {
		paths[i] = v.Path
	}
	docs, err := Snapshot(s.Root, paths, MaxTotalBytes)
	if err != nil {
		return err
	}
	for i, doc := range docs {
		if doc.SHA256 != sources[i].SHA256 {
			return fmt.Errorf("source %q changed; read the current prose and rebuild the import", doc.Path)
		}
		for _, t := range tracks {
			if t.SourcePath == doc.Path && !strings.Contains(doc.Text, t.Section) {
				return fmt.Errorf("source section %q is absent from %s", t.Section, doc.Path)
			}
		}
	}
	return nil
}

// Import validates/appends one batch atomically. A preview runs the same policy
// and validation then rolls back. Existing IDs require exact expected revisions.
// Reusing the exact replay key returns its original receipt without overwriting
// any subsequent local work; a changed bundle must use a new key.
func (s Service) Import(ctx context.Context, bundle Bundle, apply bool) (Receipt, error) {
	bundle, hash, err := normalize(bundle)
	if err != nil {
		return Receipt{}, err
	}
	if err = adoption.CheckActive(config.StateDir(s.Root)); err != nil {
		return Receipt{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback()
	var oldHash, body string
	err = tx.QueryRowContext(ctx, `SELECT bundle_sha256,receipt_json FROM prose_imports WHERE project_id=? AND id=?`, s.ProjectID, bundle.ID).Scan(&oldHash, &body)
	if err == nil {
		if oldHash != hash {
			return Receipt{}, fmt.Errorf("import id %q already names a different bundle; use a new id", bundle.ID)
		}
		var previous Receipt
		if err = json.Unmarshal([]byte(body), &previous); err != nil {
			return Receipt{}, err
		}
		previous.State = "already_applied"
		return previous, nil
	}
	if err != sql.ErrNoRows {
		return Receipt{}, err
	}
	if err = s.checkSources(bundle.Sources, bundle.Tracks); err != nil {
		return Receipt{}, err
	}
	r := Receipt{ID: bundle.ID, BundleSHA256: hash, Sources: bundle.Sources, Creator: bundle.Creator, Model: bundle.Model, RecordedAt: time.Now().UTC(), State: "preview"}
	for _, t := range bundle.Tracks {
		var nr, pr int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM roadmap_nodes WHERE project_id=? AND node_id=?`, s.ProjectID, t.ID).Scan(&nr); err != nil {
			return Receipt{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM track_plan_revisions WHERE project_id=? AND id=?`, s.ProjectID, t.PlanID).Scan(&pr); err != nil {
			return Receipt{}, err
		}
		if nr != t.ExpectedNodeRevision || pr != t.ExpectedPlanRevision {
			return Receipt{}, fmt.Errorf("track %q changed: node/plan revisions are %d/%d, expected %d/%d; reconcile current records first", t.ID, nr, pr, t.ExpectedNodeRevision, t.ExpectedPlanRevision)
		}
		var sourceHash string
		for _, source := range bundle.Sources {
			if source.Path == t.SourcePath {
				sourceHash = source.SHA256
			}
		}
		ref := fmt.Sprintf("file:%s#%s@sha256:%s", t.SourcePath, t.Section, sourceHash)
		node := roadmap.Node{ProjectID: s.ProjectID, ID: t.ID, Revision: nr + 1, Title: t.Title, Status: "proposed", Priority: 50, Kind: "work_item", SourcePath: t.SourcePath, SourceRef: ref, EvidenceRefs: []string{ref, "import_model:" + bundle.Model}}
		nodeRevision := nr
		if nr > 0 {
			var title, status, sourceRef string
			if err = tx.QueryRowContext(ctx, `SELECT title,status,COALESCE(source_ref,'') FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, s.ProjectID, t.ID, nr).Scan(&title, &status, &sourceRef); err != nil {
				return Receipt{}, err
			}
			if title != t.Title || sourceRef != ref {
				if status != "proposed" {
					return Receipt{}, fmt.Errorf("track %q has reviewed roadmap state; reconcile its source/title through roadmap review", t.ID)
				}
			} else {
				node.Revision = nr
			}
		}
		if nr == 0 || node.Revision > nr {
			if _, err = (roadmap.Service{}).AppendNodeTx(ctx, tx, node); err != nil {
				return Receipt{}, err
			}
			nodeRevision = node.Revision
		}
		if pr > 0 {
			var nodeID string
			if err = tx.QueryRowContext(ctx, `SELECT roadmap_node_id FROM track_plan_revisions WHERE project_id=? AND id=? AND revision=?`, s.ProjectID, t.PlanID, pr).Scan(&nodeID); err != nil {
				return Receipt{}, err
			}
			if nodeID != t.ID {
				return Receipt{}, fmt.Errorf("plan %q belongs to a different track; reconcile first", t.PlanID)
			}
		}
		items := append([]trackplan.Item(nil), t.Items...)
		for i := range items {
			items[i].EvidenceRefs = append([]string(nil), items[i].EvidenceRefs...)
			if !slices.Contains(items[i].EvidenceRefs, ref) {
				items[i].EvidenceRefs = append(items[i].EvidenceRefs, ref)
			}
		}
		plan := trackplan.Plan{ProjectID: s.ProjectID, ID: t.PlanID, BaseRevision: pr, Revision: pr + 1, RoadmapNodeID: t.ID, Title: t.Title, Summary: t.Summary, Status: "proposed", Creator: bundle.Creator, Model: bundle.Model, Items: items}
		written, err := s.Plans.AppendPlanTx(ctx, tx, plan)
		if err != nil {
			return Receipt{}, err
		}
		r.Tracks = append(r.Tracks, Imported{NodeID: t.ID, NodeRevision: nodeRevision, PlanID: written.ID, PlanRevision: written.Revision, PlanStatus: written.Status})
	}
	if err = s.checkSources(bundle.Sources, bundle.Tracks); err != nil {
		return Receipt{}, err
	}
	if !apply {
		return r, nil
	}
	r.State = "applied"
	b, err := json.Marshal(r)
	if err != nil {
		return Receipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO prose_imports(project_id,id,bundle_sha256,receipt_json) VALUES(?,?,?,?)`, s.ProjectID, bundle.ID, hash, string(b)); err != nil {
		return Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return r, nil
}
