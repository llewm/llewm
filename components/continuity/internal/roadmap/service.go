package roadmap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

const maxTextBytes = 16 << 10

const (
	maxBindings = 500
	maxFiles    = 2000
)

type Service struct {
	DB          *sql.DB
	Root        string
	ProjectName string
	Repo        Repository
	Now         func() time.Time
}

type roadmapQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (service Service) repository() Repository {
	if service.Repo != nil {
		return service.Repo
	}
	return GitRepository{Root: service.Root}
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func text(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if required && value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maxTextBytes {
		return "", fmt.Errorf("%s exceeds 16 KiB limit", name)
	}
	return value, nil
}

func relativePath(name, value string) (string, error) {
	value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	if value == "." || value == "" || filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("%s must be a project-relative path", name)
	}
	return value, nil
}

func (service Service) AppendNode(ctx context.Context, node Node) (Node, error) {
	return service.appendNode(ctx, service.DB, node)
}

// AppendNodeTx validates and appends within a caller-owned transaction.
func (service Service) AppendNodeTx(ctx context.Context, tx *sql.Tx, node Node) (Node, error) {
	return service.appendNode(ctx, tx, node)
}

type nodeWriter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (service Service) appendNode(ctx context.Context, db nodeWriter, node Node) (Node, error) {
	var err error
	if node.ProjectID, err = text("project ID", node.ProjectID, true); err != nil {
		return Node{}, err
	}
	if node.ID, err = text("node ID", node.ID, true); err != nil {
		return Node{}, err
	}
	if node.Title, err = text("title", node.Title, true); err != nil {
		return Node{}, err
	}
	if node.SourcePath, err = relativePath("source path", node.SourcePath); err != nil {
		return Node{}, err
	}
	if node.SourceRef, err = text("source reference", node.SourceRef, false); err != nil {
		return Node{}, err
	}
	if node.Kind == "" {
		node.Kind = "work_item"
	}
	if !oneOf(node.Kind, "initiative", "milestone", "work_item", "research") {
		return Node{}, fmt.Errorf("unsupported roadmap node kind %q", node.Kind)
	}
	if node.TargetDate, err = text("target date", node.TargetDate, false); err != nil {
		return Node{}, err
	}
	if node.TargetDate != "" {
		if parsed, parseErr := time.Parse(time.DateOnly, node.TargetDate); parseErr != nil || parsed.Format(time.DateOnly) != node.TargetDate {
			return Node{}, fmt.Errorf("target date must be YYYY-MM-DD")
		}
	}
	if node.TargetRelease, err = text("target release", node.TargetRelease, false); err != nil {
		return Node{}, err
	}
	if node.TargetHorizon, err = text("target horizon", node.TargetHorizon, false); err != nil {
		return Node{}, err
	}
	if node.Kind != "milestone" && (node.TargetDate != "" || node.TargetRelease != "" || node.TargetHorizon != "") {
		return Node{}, fmt.Errorf("target date, release and horizon are supported only for milestone nodes")
	}
	if len(node.EvidenceRefs) > 64 {
		return Node{}, fmt.Errorf("roadmap node evidence references exceed 64-item limit")
	}
	for i := range node.EvidenceRefs {
		if node.EvidenceRefs[i], err = text("evidence reference", node.EvidenceRefs[i], true); err != nil {
			return Node{}, err
		}
	}
	sort.Strings(node.EvidenceRefs)
	node.EvidenceRefs = uniqueStrings(node.EvidenceRefs)
	evidenceJSON, err := json.Marshal(node.EvidenceRefs)
	if err != nil {
		return Node{}, err
	}
	if !oneOf(node.Status, "proposed", "accepted", "active", "blocked", "completed", "deferred", "superseded") {
		return Node{}, fmt.Errorf("unsupported roadmap status %q", node.Status)
	}
	if node.Priority < 0 || node.Priority > 100 {
		return Node{}, fmt.Errorf("priority must be between 0 and 100")
	}
	if node.Revision == 0 {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM roadmap_nodes WHERE project_id = ? AND node_id = ?`, node.ProjectID, node.ID).Scan(&node.Revision); err != nil {
			return Node{}, err
		}
	}
	if node.Revision < 1 {
		return Node{}, fmt.Errorf("revision must be positive")
	}
	node.Authority, node.RecordedAt, node.SchemaVersion = "caller_asserted", service.now(), SchemaVersion
	_, err = db.ExecContext(ctx, `INSERT INTO roadmap_nodes(project_id, node_id, revision, title, status, priority, kind, target_date, target_release, target_horizon, evidence_refs_json, source_path, source_ref, authority, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, ?)`, node.ProjectID, node.ID, node.Revision, node.Title, node.Status, node.Priority, node.Kind, node.TargetDate, node.TargetRelease, node.TargetHorizon, string(evidenceJSON), node.SourcePath, node.SourceRef, node.Authority, node.RecordedAt.Format(time.RFC3339Nano), node.SchemaVersion)
	return node, err
}

func (service Service) AppendBinding(ctx context.Context, binding Binding) (Binding, error) {
	var err error
	if binding.ProjectID, err = text("project ID", binding.ProjectID, true); err != nil {
		return Binding{}, err
	}
	if binding.NodeID, err = text("node ID", binding.NodeID, true); err != nil {
		return Binding{}, err
	}
	if binding.Path, err = relativePath("binding path", binding.Path); err != nil {
		return Binding{}, err
	}
	if !oneOf(binding.PathKind, "file", "directory") {
		return Binding{}, fmt.Errorf("unsupported path kind %q", binding.PathKind)
	}
	if !oneOf(binding.Relationship, "implementation", "contract", "test", "documentation", "verification") {
		return Binding{}, fmt.Errorf("unsupported relationship %q", binding.Relationship)
	}
	if binding.NodeRevision == 0 {
		if err := service.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) FROM roadmap_nodes WHERE project_id = ? AND node_id = ?`, binding.ProjectID, binding.NodeID).Scan(&binding.NodeRevision); err != nil {
			return Binding{}, fmt.Errorf("resolve roadmap node revision: %w", err)
		}
		if binding.NodeRevision == 0 {
			return Binding{}, fmt.Errorf("roadmap node %q is not recorded", binding.NodeID)
		}
	}
	binding.ID, binding.Authority, binding.RecordedAt, binding.SchemaVersion = activity.NewID(), "caller_asserted", service.now(), SchemaVersion
	_, err = service.DB.ExecContext(ctx, `INSERT INTO roadmap_file_bindings(id, project_id, node_id, node_revision, path, path_kind, relationship, authority, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, binding.ID, binding.ProjectID, binding.NodeID, binding.NodeRevision, binding.Path, binding.PathKind, binding.Relationship, binding.Authority, binding.RecordedAt.Format(time.RFC3339Nano), binding.SchemaVersion)
	return binding, err
}

func (service Service) AppendNodeLink(ctx context.Context, link NodeLink) (NodeLink, error) {
	var err error
	if link.ProjectID, err = text("project ID", link.ProjectID, true); err != nil {
		return NodeLink{}, err
	}
	if link.NodeID, err = text("node ID", link.NodeID, true); err != nil {
		return NodeLink{}, err
	}
	if link.TargetNodeID, err = text("target node ID", link.TargetNodeID, true); err != nil {
		return NodeLink{}, err
	}
	if link.NodeID == link.TargetNodeID {
		return NodeLink{}, fmt.Errorf("roadmap node link cannot target itself")
	}
	if !oneOf(link.Relationship, "depends_on", "blocks", "parallel_with", "parent_of", "explores") {
		return NodeLink{}, fmt.Errorf("unsupported roadmap node relationship %q", link.Relationship)
	}
	if link.NodeRevision == 0 {
		if err := service.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) FROM roadmap_nodes WHERE project_id = ? AND node_id = ?`, link.ProjectID, link.NodeID).Scan(&link.NodeRevision); err != nil {
			return NodeLink{}, fmt.Errorf("resolve roadmap node revision: %w", err)
		}
	}
	if link.NodeRevision < 1 {
		return NodeLink{}, fmt.Errorf("roadmap node %q is not recorded", link.NodeID)
	}
	var targetCount int
	if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_nodes WHERE project_id = ? AND node_id = ?`, link.ProjectID, link.TargetNodeID).Scan(&targetCount); err != nil {
		return NodeLink{}, err
	}
	if targetCount == 0 {
		return NodeLink{}, fmt.Errorf("target roadmap node %q is not recorded", link.TargetNodeID)
	}
	link.ID, link.Authority, link.RecordedAt, link.SchemaVersion = activity.NewID(), "caller_asserted", service.now(), SchemaVersion
	_, err = service.DB.ExecContext(ctx, `INSERT INTO roadmap_node_links(id, project_id, node_id, node_revision, target_node_id, relationship, authority, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, link.ID, link.ProjectID, link.NodeID, link.NodeRevision, link.TargetNodeID, link.Relationship, link.Authority, link.RecordedAt.Format(time.RFC3339Nano), link.SchemaVersion)
	return link, err
}

func (service Service) AppendReviewRoutine(ctx context.Context, routine ReviewRoutine) (ReviewRoutine, error) {
	var err error
	if routine.ProjectID, err = text("project ID", routine.ProjectID, true); err != nil {
		return ReviewRoutine{}, err
	}
	if routine.ID, err = text("routine ID", routine.ID, true); err != nil {
		return ReviewRoutine{}, err
	}
	if routine.Title, err = text("title", routine.Title, true); err != nil {
		return ReviewRoutine{}, err
	}
	if routine.Source, err = text("source", routine.Source, true); err != nil {
		return ReviewRoutine{}, err
	}
	if routine.SourceRef, err = text("source reference", routine.SourceRef, false); err != nil {
		return ReviewRoutine{}, err
	}
	if !oneOf(routine.Status, "proposed", "accepted", "superseded") {
		return ReviewRoutine{}, fmt.Errorf("unsupported review routine status %q", routine.Status)
	}
	if routine.CommitThreshold == nil && routine.ElapsedSecondsThreshold == nil && routine.ChangedFilesThreshold == nil {
		return ReviewRoutine{}, fmt.Errorf("at least one review threshold is required")
	}
	if routine.CommitThreshold != nil && *routine.CommitThreshold < 1 {
		return ReviewRoutine{}, fmt.Errorf("commit threshold must be positive")
	}
	if routine.ElapsedSecondsThreshold != nil && *routine.ElapsedSecondsThreshold < 1 {
		return ReviewRoutine{}, fmt.Errorf("elapsed seconds threshold must be positive")
	}
	if routine.ChangedFilesThreshold != nil && *routine.ChangedFilesThreshold < 1 {
		return ReviewRoutine{}, fmt.Errorf("changed files threshold must be positive")
	}
	if routine.Revision == 0 {
		if err := service.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM roadmap_review_routines WHERE project_id = ? AND routine_id = ?`, routine.ProjectID, routine.ID).Scan(&routine.Revision); err != nil {
			return ReviewRoutine{}, err
		}
	}
	if routine.Revision < 1 {
		return ReviewRoutine{}, fmt.Errorf("revision must be positive")
	}
	routine.Authority, routine.RecordedAt, routine.SchemaVersion = "caller_asserted", service.now(), SchemaVersion
	_, err = service.DB.ExecContext(ctx, `INSERT INTO roadmap_review_routines(project_id, routine_id, revision, title, status, commit_threshold, elapsed_seconds_threshold, changed_files_threshold, source, source_ref, authority, recorded_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`, routine.ProjectID, routine.ID, routine.Revision, routine.Title, routine.Status, routine.CommitThreshold, routine.ElapsedSecondsThreshold, routine.ChangedFilesThreshold, routine.Source, routine.SourceRef, routine.Authority, routine.RecordedAt.Format(time.RFC3339Nano), routine.SchemaVersion)
	return routine, err
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

type bindingView struct {
	Binding
	NodeTitle string
}

func (service Service) currentBindings(ctx context.Context, projectID string) ([]bindingView, error) {
	return currentBindingsFrom(ctx, service.DB, projectID)
}

func currentBindingsFrom(ctx context.Context, db roadmapQueryer, projectID string) ([]bindingView, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.id, b.node_id, b.node_revision, b.path, b.path_kind, b.relationship, b.authority, b.recorded_at, b.schema_version, n.title
		FROM roadmap_file_bindings b
		JOIN roadmap_nodes n ON n.project_id = b.project_id AND n.node_id = b.node_id AND n.revision = b.node_revision
		WHERE b.project_id = ? AND n.revision = (SELECT MAX(latest.revision) FROM roadmap_nodes latest WHERE latest.project_id = n.project_id AND latest.node_id = n.node_id)
		ORDER BY b.path, b.node_id, b.relationship LIMIT ?`, projectID, maxBindings+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]bindingView, 0)
	for rows.Next() {
		var binding bindingView
		var recorded string
		if err := rows.Scan(&binding.ID, &binding.NodeID, &binding.NodeRevision, &binding.Path, &binding.PathKind, &binding.Relationship, &binding.Authority, &recorded, &binding.SchemaVersion, &binding.NodeTitle); err != nil {
			return nil, err
		}
		binding.ProjectID = projectID
		if binding.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
		if len(bindings) > maxBindings {
			return nil, fmt.Errorf("current roadmap bindings exceed %d-item limit", maxBindings)
		}
	}
	return bindings, rows.Err()
}

type fileState struct {
	hash  string
	state string
}

func (service Service) capture(ctx context.Context, bindings []bindingView) (map[string]fileState, map[string][]bindingView, error) {
	states := map[string]fileState{}
	owners := map[string][]bindingView{}
	for _, binding := range bindings {
		paths := []string{binding.Path}
		if binding.PathKind == "directory" {
			var err error
			paths, err = service.repository().Files(ctx, binding.Path)
			if err != nil {
				return nil, nil, err
			}
		}
		for _, path := range paths {
			state, err := service.hashPath(path)
			if err != nil {
				return nil, nil, err
			}
			states[path] = state
			owners[path] = append(owners[path], binding)
			if len(states) > maxFiles {
				return nil, nil, fmt.Errorf("bound roadmap files exceed %d-item limit", maxFiles)
			}
		}
	}
	return states, owners, nil
}

func (service Service) hashPath(relative string) (fileState, error) {
	path := filepath.Join(service.Root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fileState{state: "missing"}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return fileState{}, err
		}
		sum := sha256.Sum256([]byte("symlink:" + target))
		return fileState{hash: hex.EncodeToString(sum[:]), state: "present"}, nil
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("bound path %s is not a regular file", relative)
	}
	if info.Size() > 16<<20 {
		return fileState{}, fmt.Errorf("bound file %s exceeds 16 MiB hash limit", relative)
	}
	file, err := os.Open(path)
	if err != nil {
		return fileState{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fileState{}, err
	}
	return fileState{hash: hex.EncodeToString(hash.Sum(nil)), state: "present"}, nil
}

func (service Service) RecordSync(ctx context.Context, run SyncRun) (SyncRun, error) {
	var err error
	if run.ProjectID, err = text("project ID", run.ProjectID, true); err != nil {
		return SyncRun{}, err
	}
	for name, field := range map[string]*string{"client": &run.Client, "model": &run.Model, "invocation ID": &run.InvocationID} {
		if *field, err = text(name, *field, false); err != nil {
			return SyncRun{}, err
		}
	}
	if run.Model == "" {
		return SyncRun{}, fmt.Errorf("model is required for a model-sync record")
	}
	if len(run.ReviewedNodes) > 64 || len(run.ReviewedFiles) > 64 {
		return SyncRun{}, fmt.Errorf("reviewed nodes and files are limited to 64 entries each")
	}
	if len(run.ReviewedNodes) == 0 || len(run.ReviewedFiles) == 0 {
		return SyncRun{}, fmt.Errorf("at least one reviewed node and reviewed file are required")
	}
	for _, values := range [][]string{run.ReviewedNodes, run.ReviewedFiles} {
		for index := range values {
			if values[index], err = text("review reference", values[index], true); err != nil {
				return SyncRun{}, err
			}
		}
	}
	for index := range run.ReviewedFiles {
		if run.ReviewedFiles[index], err = relativePath("reviewed file", run.ReviewedFiles[index]); err != nil {
			return SyncRun{}, err
		}
	}
	for _, nodeID := range run.ReviewedNodes {
		var count int
		if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_nodes WHERE project_id = ? AND node_id = ?`, run.ProjectID, nodeID).Scan(&count); err != nil {
			return SyncRun{}, err
		}
		if count == 0 {
			return SyncRun{}, fmt.Errorf("reviewed roadmap node %q is not recorded", nodeID)
		}
	}
	run.GitCommit, err = service.repository().Head(ctx)
	if err != nil {
		return SyncRun{}, err
	}
	bindings, err := service.currentBindings(ctx, run.ProjectID)
	if err != nil {
		return SyncRun{}, err
	}
	states, _, err := service.capture(ctx, bindings)
	if err != nil {
		return SyncRun{}, err
	}
	run.ID, run.Authority, run.GitAuthority, run.CompletedAt, run.SchemaVersion = activity.NewID(), "caller_asserted", "machine_observed", service.now(), SchemaVersion
	nodesJSON, _ := json.Marshal(nonNil(run.ReviewedNodes))
	filesJSON, _ := json.Marshal(nonNil(run.ReviewedFiles))
	tx, err := service.DB.BeginTx(ctx, nil)
	if err != nil {
		return SyncRun{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO roadmap_sync_runs(id, project_id, git_commit, client, model, invocation_id, reviewed_nodes_json, reviewed_files_json, authority, git_authority, completed_at, schema_version) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`, run.ID, run.ProjectID, run.GitCommit, run.Client, run.Model, run.InvocationID, string(nodesJSON), string(filesJSON), run.Authority, run.GitAuthority, run.CompletedAt.Format(time.RFC3339Nano), run.SchemaVersion); err != nil {
		return SyncRun{}, err
	}
	paths := sortedKeys(states)
	for _, path := range paths {
		state := states[path]
		if _, err = tx.ExecContext(ctx, `INSERT INTO roadmap_sync_files(sync_id, project_id, path, content_hash, state) VALUES (?, ?, ?, NULLIF(?, ''), ?)`, run.ID, run.ProjectID, path, state.hash, state.state); err != nil {
			return SyncRun{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SyncRun{}, err
	}
	run.ReviewedNodes, run.ReviewedFiles = nonNil(run.ReviewedNodes), nonNil(run.ReviewedFiles)
	return run, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
