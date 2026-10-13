package roadmap

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

const (
	DefaultTreeLimit = 10
	MaxTreeLimit     = 64
)

type TreeOptions struct {
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit,omitempty"`
}

type RoadmapTree struct {
	SchemaVersion            int               `json:"schema_version"`
	ProjectID                string            `json:"project_id"`
	Nodes                    int               `json:"nodes"`
	HierarchyRoots           int               `json:"hierarchy_roots"`
	Entries                  []RoadmapTreeNode `json:"entries"`
	UnrootedNodes            []RoadmapTreeNode `json:"unrooted_nodes"`
	DanglingTargets          []DanglingTarget  `json:"dangling_targets"`
	NodesWithMultipleParents int               `json:"nodes_with_multiple_parents"`
	HierarchyCycleNodes      int               `json:"hierarchy_cycle_nodes"`
	Offset                   int               `json:"offset"`
	Limit                    int               `json:"limit"`
	Total                    int               `json:"total_roots"`
	Returned                 int               `json:"returned_roots"`
	Omitted                  int               `json:"omitted_roots"`
	Truncated                bool              `json:"truncated"`
	Authority                string            `json:"authority"`
	Disclaimer               string            `json:"disclaimer"`
}

type RoadmapTreeNode struct {
	NodeRef
	Revision     int       `json:"revision"`
	ParentID     string    `json:"parent_id,omitempty"`
	Depth        int       `json:"depth"`
	Parents      []NodeRef `json:"parents,omitempty"`
	Reference    bool      `json:"reference,omitempty"`
	Observations []string  `json:"observations"`
}

type DanglingTarget struct {
	NodeID       string `json:"node_id"`
	TargetNodeID string `json:"target_node_id"`
}

// Tree returns a bounded hierarchy view over current roadmap node revisions.
// It follows parent_of links only and does not change saved records.
func (service Service) Tree(ctx context.Context, projectID string, options TreeOptions) (RoadmapTree, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return RoadmapTree{}, err
	}
	if options.Limit == 0 {
		options.Limit = DefaultTreeLimit
	}
	if options.Offset < 0 || options.Limit < 1 || options.Limit > MaxTreeLimit {
		return RoadmapTree{}, fmt.Errorf("roadmap tree offset must be nonnegative and limit must be between 1 and %d", MaxTreeLimit)
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RoadmapTree{}, err
	}
	defer tx.Rollback()
	nodes, err := currentNodesFrom(ctx, tx, projectID)
	if err != nil {
		return RoadmapTree{}, err
	}
	links, truncated, err := currentNodeLinksFrom(ctx, tx, projectID, maxProjectionLinks)
	if err != nil {
		return RoadmapTree{}, err
	}
	if truncated {
		return RoadmapTree{}, fmt.Errorf("current roadmap relationships exceed %d-item tree limit", maxProjectionLinks)
	}
	if err := tx.Commit(); err != nil {
		return RoadmapTree{}, err
	}
	nodeByID := make(map[string]Node, len(nodes))
	order := make(map[string]int, len(nodes))
	for i, node := range nodes {
		nodeByID[node.ID], order[node.ID] = node, i
	}
	children := make(map[string][]string, len(nodes))
	parents := make(map[string][]NodeRef, len(nodes))
	dangling := make([]DanglingTarget, 0)
	for _, link := range links {
		if link.Relationship != "parent_of" {
			continue
		}
		parent, parentOK := nodeByID[link.NodeID]
		child, childOK := nodeByID[link.TargetNodeID]
		if !parentOK {
			continue
		}
		if !childOK {
			dangling = append(dangling, DanglingTarget{NodeID: parent.ID, TargetNodeID: link.TargetNodeID})
			continue
		}
		children[parent.ID] = appendUnique(children[parent.ID], child.ID)
		parents[child.ID] = appendNodeRef(parents[child.ID], nodeRef(parent))
	}
	for id := range children {
		sort.Slice(children[id], func(i, j int) bool { return order[children[id][i]] < order[children[id][j]] })
	}
	sort.Slice(dangling, func(i, j int) bool {
		if dangling[i].NodeID != dangling[j].NodeID {
			return dangling[i].NodeID < dangling[j].NodeID
		}
		return dangling[i].TargetNodeID < dangling[j].TargetNodeID
	})
	cycleInput := make(map[string]*CoverageNode, len(nodes))
	for _, node := range nodes {
		cycleInput[node.ID] = &CoverageNode{ID: node.ID}
	}
	cycleNodes := hierarchyCycleNodes(cycleInput, children)
	roots := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if len(parents[node.ID]) == 0 {
			roots = append(roots, node.ID)
		}
	}
	start := options.Offset
	if start > len(roots) {
		start = len(roots)
	}
	end := start + options.Limit
	if end > len(roots) {
		end = len(roots)
	}
	expanded := make(map[string]bool, len(nodes))
	entries := make([]RoadmapTreeNode, 0, len(nodes))
	var build func(string, string, int, map[string]bool)
	build = func(id, parentID string, depth int, ancestry map[string]bool) {
		node := nodeByID[id]
		item := RoadmapTreeNode{NodeRef: nodeRef(node), Revision: node.Revision, ParentID: parentID, Depth: depth, Parents: append([]NodeRef(nil), parents[id]...), Observations: []string{}}
		if ancestry[id] {
			item.Reference = true
			item.Observations = append(item.Observations, "hierarchy_cycle")
			entries = append(entries, item)
			return
		}
		if expanded[id] {
			item.Reference = true
			item.Observations = append(item.Observations, "already_expanded")
			entries = append(entries, item)
			return
		}
		expanded[id] = true
		if len(parents[id]) > 1 {
			item.Observations = append(item.Observations, "multiple_hierarchy_parents")
		}
		if cycleNodes[id] {
			item.Observations = append(item.Observations, "hierarchy_cycle")
		}
		entries = append(entries, item)
		nextAncestry := make(map[string]bool, len(ancestry)+1)
		for ancestor := range ancestry {
			nextAncestry[ancestor] = true
		}
		nextAncestry[id] = true
		for _, childID := range children[id] {
			build(childID, id, depth+1, nextAncestry)
		}
	}
	for _, rootID := range roots[start:end] {
		build(rootID, "", 0, map[string]bool{})
	}
	reachable := make(map[string]bool, len(nodes))
	var markReachable func(string)
	markReachable = func(id string) {
		if reachable[id] {
			return
		}
		reachable[id] = true
		for _, childID := range children[id] {
			markReachable(childID)
		}
	}
	for _, rootID := range roots {
		markReachable(rootID)
	}
	unrooted := make([]RoadmapTreeNode, 0)
	for _, node := range nodes {
		if reachable[node.ID] {
			continue
		}
		item := RoadmapTreeNode{NodeRef: nodeRef(node), Revision: node.Revision, Depth: 0, Parents: append([]NodeRef(nil), parents[node.ID]...), Reference: true, Observations: []string{"unrooted_component"}}
		if cycleNodes[node.ID] {
			item.Observations = append(item.Observations, "hierarchy_cycle")
		}
		if len(parents[node.ID]) > 1 {
			item.Observations = append(item.Observations, "multiple_hierarchy_parents")
		}
		unrooted = append(unrooted, item)
	}
	multipleParents := 0
	for _, refs := range parents {
		if len(refs) > 1 {
			multipleParents++
		}
	}
	return RoadmapTree{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Nodes: len(nodes), HierarchyRoots: len(roots), Entries: append(entries, unrooted...),
		UnrootedNodes: unrooted, DanglingTargets: dangling, NodesWithMultipleParents: multipleParents, HierarchyCycleNodes: len(cycleNodes),
		Offset: options.Offset, Limit: options.Limit, Total: len(roots), Returned: end - start, Omitted: len(roots) - (end - start), Truncated: len(roots) > end-start,
		Authority: "mixed", Disclaimer: "The tree follows current-revision parent_of links only. Other relationship types are omitted here and remain available through roadmap view coverage. Repeated nodes are compact references; cycles, multiple parents, dangling targets, and unrooted components are reported as observations, not inferred errors or repair instructions.",
	}, nil
}
