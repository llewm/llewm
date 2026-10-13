package roadmap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultCoverageLimit = 20
	MaxCoverageLimit     = 64
)

type CoverageOptions struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

type Coverage struct {
	SchemaVersion               int            `json:"schema_version"`
	ProjectID                   string         `json:"project_id"`
	CurrentNodes                int            `json:"current_nodes"`
	NodesWithCurrentBindings    int            `json:"nodes_with_current_bindings"`
	NodesWithoutCurrentBindings int            `json:"nodes_without_current_bindings"`
	CurrentRevisionBindings     int            `json:"current_revision_bindings"`
	PriorRevisionBindings       int            `json:"prior_revision_bindings"`
	CurrentRelationships        int            `json:"current_relationships"`
	PriorRevisionRelationships  int            `json:"prior_revision_relationships"`
	DanglingRelationshipTargets int            `json:"dangling_relationship_targets"`
	HierarchyRoots              int            `json:"hierarchy_roots"`
	NodesWithMultipleParents    int            `json:"nodes_with_multiple_parents"`
	HierarchyCycleNodes         int            `json:"hierarchy_cycle_nodes"`
	Nodes                       []CoverageNode `json:"nodes"`
	Offset                      int            `json:"offset"`
	Limit                       int            `json:"limit"`
	Total                       int            `json:"total"`
	Returned                    int            `json:"returned"`
	Omitted                     int            `json:"omitted"`
	Truncated                   bool           `json:"truncated"`
	SourcePathsChecked          bool           `json:"source_paths_checked"`
	SourceComparisonBytes       int64          `json:"source_comparison_bytes"`
	Authority                   string         `json:"authority"`
	Disclaimer                  string         `json:"disclaimer"`
}

type CoverageNode struct {
	ID                         string           `json:"id"`
	Revision                   int              `json:"revision"`
	Title                      string           `json:"title"`
	SourcePath                 string           `json:"source_path"`
	SourceRef                  string           `json:"source_ref,omitempty"`
	SourcePathState            string           `json:"source_path_state"`
	SourceComparison           string           `json:"source_comparison"`
	Status                     string           `json:"status"`
	Priority                   int              `json:"priority"`
	Kind                       string           `json:"kind"`
	TargetDate                 string           `json:"target_date,omitempty"`
	TargetRelease              string           `json:"target_release,omitempty"`
	TargetHorizon              string           `json:"target_horizon,omitempty"`
	EvidenceRefs               []string         `json:"evidence_refs"`
	Milestone                  *MilestoneRollup `json:"milestone,omitempty"`
	CurrentBindings            int              `json:"current_bindings"`
	PriorRevisionBindings      int              `json:"prior_revision_bindings"`
	PriorRevisionRelationships int              `json:"prior_revision_relationships"`
	Relationships              map[string]int   `json:"relationships"`
	Parents                    []NodeRef        `json:"parents"`
	Children                   []NodeRef        `json:"children"`
	DanglingTargets            []string         `json:"dangling_targets"`
	MultipleParents            bool             `json:"multiple_parents"`
	InHierarchyCycle           bool             `json:"in_hierarchy_cycle"`
	ExpectedScope              []ScopePath      `json:"expected_scope"`
	ObservedTouches            []string         `json:"observed_touches"`
	ExpectedWithoutObserved    []string         `json:"expected_without_observed"`
	ObservedOutsideExpected    []string         `json:"observed_outside_expected"`
	ScopeEvidenceTruncated     bool             `json:"scope_evidence_truncated"`
	Observations               []string         `json:"observations"`
}

type MilestoneRollup struct {
	ChildCount        int      `json:"child_count"`
	CompletedChildren int      `json:"completed_children"`
	ActiveChildren    int      `json:"active_children"`
	BlockedChildren   int      `json:"blocked_children"`
	PendingChildren   int      `json:"pending_children"`
	BlockerNodeIDs    []string `json:"blocker_node_ids"`
	EvidenceRefs      []string `json:"evidence_refs"`
	EvidenceTruncated bool     `json:"evidence_truncated"`
}

type ScopePath struct {
	Path         string `json:"path"`
	PathKind     string `json:"path_kind"`
	Relationship string `json:"relationship"`
}

// Coverage reports bounded structural facts and source comparisons for current roadmap revisions.
// Comparisons are limited to returned nodes and exact full Git commit IDs.
func (service Service) Coverage(ctx context.Context, projectID string, options CoverageOptions) (Coverage, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return Coverage{}, err
	}
	if options.Limit == 0 {
		options.Limit = DefaultCoverageLimit
	}
	if options.Offset < 0 || options.Limit < 1 || options.Limit > MaxCoverageLimit {
		return Coverage{}, fmt.Errorf("coverage offset must be nonnegative and limit must be between 1 and %d", MaxCoverageLimit)
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Coverage{}, err
	}
	defer tx.Rollback()
	nodes, err := currentNodesFrom(ctx, tx, projectID)
	if err != nil {
		return Coverage{}, err
	}
	bindings, err := currentBindingsFrom(ctx, tx, projectID)
	if err != nil {
		return Coverage{}, err
	}
	links, truncated, err := currentNodeLinksFrom(ctx, tx, projectID, maxProjectionLinks)
	if err != nil {
		return Coverage{}, err
	}
	if truncated {
		return Coverage{}, fmt.Errorf("current roadmap relationships exceed %d-item coverage limit", maxProjectionLinks)
	}
	priorBindings, err := priorRevisionCounts(ctx, tx, "roadmap_file_bindings", projectID)
	if err != nil {
		return Coverage{}, err
	}
	priorLinks, err := priorRevisionCounts(ctx, tx, "roadmap_node_links", projectID)
	if err != nil {
		return Coverage{}, err
	}
	if err := tx.Commit(); err != nil {
		return Coverage{}, err
	}

	nodeByID := make(map[string]Node, len(nodes))
	coverageByID := make(map[string]*CoverageNode, len(nodes))
	ordered := make([]CoverageNode, 0, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
		coverageByID[node.ID] = &CoverageNode{
			ID: node.ID, Revision: node.Revision, Title: node.Title, Status: node.Status, Priority: node.Priority,
			Kind: node.Kind, TargetDate: node.TargetDate, TargetRelease: node.TargetRelease, TargetHorizon: node.TargetHorizon, EvidenceRefs: append([]string{}, node.EvidenceRefs...),
			SourcePath: node.SourcePath, SourceRef: node.SourceRef,
			Relationships: map[string]int{}, Parents: []NodeRef{}, Children: []NodeRef{}, DanglingTargets: []string{}, ExpectedScope: []ScopePath{}, ObservedTouches: []string{}, ExpectedWithoutObserved: []string{}, ObservedOutsideExpected: []string{}, Observations: []string{},
			PriorRevisionBindings: priorBindings[node.ID], PriorRevisionRelationships: priorLinks[node.ID],
		}
	}
	result := Coverage{
		SchemaVersion: SchemaVersion, ProjectID: projectID, CurrentNodes: len(nodes),
		PriorRevisionBindings: sumCounts(priorBindings), PriorRevisionRelationships: sumCounts(priorLinks),
		Authority: "mixed", Offset: options.Offset, Limit: options.Limit, Total: len(nodes),
		Nodes: []CoverageNode{}, SourcePathsChecked: true,
		Disclaimer: "Counts describe saved roadmap revisions and relationships. Expected scope comes from current-revision file bindings; observed touches come only from file anchors on current work narratives linked to the same roadmap node, bounded to 64 paths per returned node. Missing or outside touches are review candidates, not correctness, ownership, completion, or execution judgments. Source comparisons apply only to the returned page, compare current file bytes with the path at an exact available full Git commit ID, and read at most 16 MiB of combined current and historical bytes per page.",
	}
	currentBindingRows := 0
	blockersByNode := make(map[string][]string, len(nodes))
	for _, binding := range bindings {
		coverageNode, ok := coverageByID[binding.NodeID]
		if !ok || nodeByID[binding.NodeID].Revision != binding.NodeRevision {
			continue
		}
		coverageNode.CurrentBindings++
		coverageNode.ExpectedScope = append(coverageNode.ExpectedScope, ScopePath{Path: binding.Path, PathKind: binding.PathKind, Relationship: binding.Relationship})
		currentBindingRows++
	}
	result.CurrentRevisionBindings = currentBindingRows
	for _, coverageNode := range coverageByID {
		if coverageNode.CurrentBindings > 0 {
			result.NodesWithCurrentBindings++
		} else {
			result.NodesWithoutCurrentBindings++
			coverageNode.Observations = append(coverageNode.Observations, "no_current_revision_binding")
		}
		if coverageNode.PriorRevisionBindings > 0 {
			coverageNode.Observations = append(coverageNode.Observations, "prior_revision_bindings")
		}
		if priorLinks[coverageNode.ID] > 0 {
			coverageNode.Observations = append(coverageNode.Observations, "prior_revision_relationships")
		}
	}
	for _, link := range links {
		source, ok := coverageByID[link.NodeID]
		if !ok {
			continue
		}
		source.Relationships[link.Relationship]++
		result.CurrentRelationships++
		target, targetOK := nodeByID[link.TargetNodeID]
		if !targetOK {
			result.DanglingRelationshipTargets++
			source.DanglingTargets = append(source.DanglingTargets, link.TargetNodeID)
			source.Observations = append(source.Observations, "dangling_relationship_target")
			continue
		}
		switch link.Relationship {
		case "parent_of":
			source.Children = appendNodeRef(source.Children, nodeRef(target))
			targetCoverage := coverageByID[target.ID]
			targetCoverage.Parents = appendNodeRef(targetCoverage.Parents, nodeRef(nodeByID[link.NodeID]))
		case "depends_on":
			if target.Status != "completed" {
				source.Observations = append(source.Observations, "unresolved_dependency")
				blockersByNode[source.ID] = append(blockersByNode[source.ID], target.ID)
			}
		case "blocks":
			blockersByNode[target.ID] = append(blockersByNode[target.ID], source.ID)
		}
	}
	adjacency := make(map[string][]string, len(nodes))
	for _, node := range coverageByID {
		if len(node.Parents) == 0 {
			result.HierarchyRoots++
		}
		if len(node.Parents) > 1 {
			result.NodesWithMultipleParents++
			node.MultipleParents = true
			node.Observations = append(node.Observations, "multiple_hierarchy_parents")
		}
		for _, child := range node.Children {
			adjacency[node.ID] = append(adjacency[node.ID], child.ID)
		}
		sortTrack(&Track{Parents: node.Parents, Subtasks: node.Children})
	}
	for _, node := range coverageByID {
		if node.Kind != "milestone" {
			continue
		}
		rollup := &MilestoneRollup{BlockerNodeIDs: []string{}, EvidenceRefs: append([]string{}, node.EvidenceRefs...)}
		for _, childRef := range node.Children {
			child := nodeByID[childRef.ID]
			rollup.ChildCount++
			switch child.Status {
			case "completed":
				rollup.CompletedChildren++
				rollup.EvidenceRefs = append(rollup.EvidenceRefs, child.EvidenceRefs...)
			case "active", "accepted":
				rollup.ActiveChildren++
			case "blocked":
				rollup.BlockedChildren++
				rollup.BlockerNodeIDs = append(rollup.BlockerNodeIDs, child.ID)
			default:
				rollup.PendingChildren++
			}
			rollup.BlockerNodeIDs = append(rollup.BlockerNodeIDs, blockersByNode[child.ID]...)
		}
		sort.Strings(rollup.BlockerNodeIDs)
		rollup.BlockerNodeIDs = uniqueStrings(rollup.BlockerNodeIDs)
		sort.Strings(rollup.EvidenceRefs)
		rollup.EvidenceRefs = uniqueStrings(rollup.EvidenceRefs)
		if len(rollup.EvidenceRefs) > 64 {
			rollup.EvidenceRefs = rollup.EvidenceRefs[:64]
			rollup.EvidenceTruncated = true
		}
		node.Milestone = rollup
	}
	cycleNodes := hierarchyCycleNodes(coverageByID, adjacency)
	result.HierarchyCycleNodes = len(cycleNodes)
	for id := range cycleNodes {
		node := coverageByID[id]
		node.InHierarchyCycle = true
		node.Observations = append(node.Observations, "hierarchy_cycle")
	}
	for _, node := range coverageByID {
		sort.Strings(node.DanglingTargets)
		sort.Strings(node.Observations)
		node.Observations = uniqueStrings(node.Observations)
	}
	for _, node := range nodes {
		ordered = append(ordered, *coverageByID[node.ID])
	}
	start := options.Offset
	if start > len(ordered) {
		start = len(ordered)
	}
	end := start + options.Limit
	if end > len(ordered) {
		end = len(ordered)
	}
	result.Nodes = ordered[start:end]
	const maxCoverageComparisonBytes int64 = 16 << 20
	for i := range result.Nodes {
		if err := service.addScopeEvidence(ctx, projectID, &result.Nodes[i]); err != nil {
			return Coverage{}, fmt.Errorf("compare roadmap scope for %s: %w", result.Nodes[i].ID, err)
		}
		state, err := service.inspectSourcePath(result.Nodes[i].SourcePath)
		if err != nil {
			return Coverage{}, fmt.Errorf("inspect roadmap source path %s: %w", result.Nodes[i].SourcePath, err)
		}
		result.Nodes[i].SourcePathState = state
		comparison, used, err := service.compareSource(ctx, result.Nodes[i], maxCoverageComparisonBytes-result.SourceComparisonBytes)
		if err != nil {
			return Coverage{}, fmt.Errorf("compare roadmap source %s: %w", result.Nodes[i].SourcePath, err)
		}
		result.Nodes[i].SourceComparison = comparison
		result.SourceComparisonBytes += used
	}
	result.Returned = len(result.Nodes)
	result.Omitted = len(ordered) - result.Returned
	result.Truncated = result.Omitted > 0
	return result, nil
}

const maxScopeEvidencePaths = 64

func (service Service) addScopeEvidence(ctx context.Context, projectID string, node *CoverageNode) error {
	sort.Slice(node.ExpectedScope, func(i, j int) bool {
		if node.ExpectedScope[i].Path != node.ExpectedScope[j].Path {
			return node.ExpectedScope[i].Path < node.ExpectedScope[j].Path
		}
		return node.ExpectedScope[i].Relationship < node.ExpectedScope[j].Relationship
	})
	rows, err := service.DB.QueryContext(ctx, `SELECT DISTINCT f.target
		FROM work_narrative_anchors r
		JOIN work_narrative_anchors f ON f.project_id=r.project_id AND f.narrative_id=r.narrative_id
		WHERE r.project_id=? AND r.anchor_kind='roadmap_node' AND r.target=? AND f.anchor_kind='file'
		AND NOT EXISTS(SELECT 1 FROM superseded_records s WHERE s.project_id=r.project_id AND s.family='narrative' AND s.id=r.narrative_id)
		ORDER BY f.target LIMIT ?`, projectID, node.ID, maxScopeEvidencePaths+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if len(node.ObservedTouches) == maxScopeEvidencePaths {
			node.ScopeEvidenceTruncated = true
			continue
		}
		node.ObservedTouches = append(node.ObservedTouches, path)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, expected := range node.ExpectedScope {
		matched := false
		for _, observed := range node.ObservedTouches {
			if scopeContains(expected, observed) {
				matched = true
				break
			}
		}
		if !matched {
			node.ExpectedWithoutObserved = append(node.ExpectedWithoutObserved, expected.Path)
		}
	}
	for _, observed := range node.ObservedTouches {
		matched := false
		for _, expected := range node.ExpectedScope {
			if scopeContains(expected, observed) {
				matched = true
				break
			}
		}
		if !matched {
			node.ObservedOutsideExpected = append(node.ObservedOutsideExpected, observed)
		}
	}
	if len(node.ExpectedWithoutObserved) > 0 {
		node.Observations = append(node.Observations, "expected_scope_without_observed_touch")
	}
	if len(node.ObservedOutsideExpected) > 0 {
		node.Observations = append(node.Observations, "observed_touch_outside_expected_scope")
	}
	if node.ScopeEvidenceTruncated {
		node.Observations = append(node.Observations, "scope_evidence_truncated")
	}
	sort.Strings(node.Observations)
	node.Observations = uniqueStrings(node.Observations)
	return nil
}

func scopeContains(expected ScopePath, observed string) bool {
	if expected.PathKind == "directory" {
		return observed == expected.Path || strings.HasPrefix(observed, strings.TrimSuffix(expected.Path, "/")+"/")
	}
	return observed == expected.Path
}

const maxSourceComparisonBytes = 16 << 20

func (service Service) compareSource(ctx context.Context, node CoverageNode, budget int64) (string, int64, error) {
	if node.SourceRef == "" {
		return "no_reference", 0, nil
	}
	if !exactCommitID.MatchString(node.SourceRef) {
		return "unsupported_reference", 0, nil
	}
	if node.SourcePathState != "regular_file" {
		return "current_path_unavailable", 0, nil
	}
	cleanPath, err := relativePath("source path", node.SourcePath)
	if err != nil || cleanPath != node.SourcePath {
		return "invalid_path", 0, nil
	}
	path := filepath.Join(service.Root, filepath.FromSlash(node.SourcePath))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "current_path_unavailable", 0, nil
	}
	if info.Size() > maxSourceComparisonBytes {
		return "too_large", 0, nil
	}
	if budget < 2 {
		return "comparison_budget_exhausted", 0, nil
	}
	perSide := budget / 2
	if perSide > maxSourceComparisonBytes {
		perSide = maxSourceComparisonBytes
	}
	if info.Size() > perSide {
		return "comparison_budget_exhausted", 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "current_path_unavailable", 0, nil
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || !os.SameFile(info, fileInfo) || fileInfo.Size() > perSide {
		return "current_path_unavailable", 0, nil
	}
	historical, state, err := service.repository().ContentAtRevision(ctx, node.SourceRef, node.SourcePath, perSide)
	if err != nil {
		return "", 0, err
	}
	if state != "available" {
		if state == "too_large" && perSide < maxSourceComparisonBytes {
			return "comparison_budget_exhausted", 0, nil
		}
		return state, 0, nil
	}
	if int64(len(historical)) > perSide {
		return "comparison_budget_exhausted", 0, nil
	}
	current, err := io.ReadAll(io.LimitReader(file, perSide+1))
	if err != nil {
		return "current_path_unavailable", int64(len(historical) + len(current)), nil
	}
	if int64(len(current)) > perSide {
		return "comparison_budget_exhausted", int64(len(historical) + len(current)), nil
	}
	used := int64(len(historical) + len(current))
	if sha256.Sum256(historical) == sha256.Sum256(current) {
		return "matches_reference", used, nil
	}
	return "differs_from_reference", used, nil
}

func (service Service) inspectSourcePath(relative string) (string, error) {
	clean, err := relativePath("source path", relative)
	if err != nil {
		return "invalid", nil
	}
	root, err := filepath.Abs(service.Root)
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.FromSlash(clean), string(filepath.Separator))
	current := root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return "missing", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "symlink", nil
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "not_directory", nil
		}
		if i == len(parts)-1 {
			if info.Mode().IsRegular() {
				return "regular_file", nil
			}
			return "other_type", nil
		}
	}
	return "missing", nil
}

func priorRevisionCounts(ctx context.Context, db roadmapQueryer, table, projectID string) (map[string]int, error) {
	if table != "roadmap_file_bindings" && table != "roadmap_node_links" {
		return nil, fmt.Errorf("unsupported roadmap coverage table")
	}
	query := fmt.Sprintf(`SELECT old.node_id, COUNT(*) FROM %s old
		JOIN roadmap_nodes latest ON latest.project_id=old.project_id AND latest.node_id=old.node_id
		AND latest.revision=(SELECT MAX(n.revision) FROM roadmap_nodes n WHERE n.project_id=old.project_id AND n.node_id=old.node_id)
		WHERE old.project_id=? AND old.node_revision < latest.revision GROUP BY old.node_id ORDER BY old.node_id`, table)
	rows, err := db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hierarchyCycleNodes(nodes map[string]*CoverageNode, adjacency map[string][]string) map[string]bool {
	state := make(map[string]uint8, len(nodes))
	positions := make(map[string]int, len(nodes))
	stack := make([]string, 0, len(nodes))
	cycles := make(map[string]bool)
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		positions[id] = len(stack)
		stack = append(stack, id)
		for _, child := range adjacency[id] {
			if _, ok := nodes[child]; !ok {
				continue
			}
			switch state[child] {
			case 0:
				visit(child)
			case 1:
				for _, cycleID := range stack[positions[child]:] {
					cycles[cycleID] = true
				}
			}
		}
		stack = stack[:len(stack)-1]
		delete(positions, id)
		state[id] = 2
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == 0 {
			visit(id)
		}
	}
	return cycles
}
