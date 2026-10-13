package roadmap

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

const (
	DefaultOverviewLimit = 20
	MaxOverviewLimit     = 64
)

func (service Service) Overview(ctx context.Context, projectID string, limit int) (Overview, error) {
	if _, err := text("project ID", projectID, true); err != nil {
		return Overview{}, err
	}
	if limit == 0 {
		limit = DefaultOverviewLimit
	}
	if limit < 1 || limit > MaxOverviewLimit {
		return Overview{}, fmt.Errorf("roadmap overview limit must be between 1 and %d", MaxOverviewLimit)
	}
	status, err := service.Status(ctx, projectID)
	if err != nil {
		return Overview{}, err
	}
	nodes, err := service.currentNodes(ctx, projectID)
	if err != nil {
		return Overview{}, err
	}
	result := Overview{SchemaVersion: SchemaVersion, ProjectID: projectID, Status: status, CurrentFocus: []Node{}, Blocked: []Node{}, Future: []Node{}, Completed: []Node{}, ChangedNodesSinceSync: []Node{}, Relationships: []NodeLink{}, Tracks: []Track{}, ReviewTriggers: reviewTriggers(status), Limit: limit, Disclaimer: "Roadmap records are caller-asserted; tracks and trigger states are deterministic derived views; repository freshness is machine-observed. This bounded overview does not reevaluate roadmap prose, invoke a model, or execute tasks."}
	for _, node := range nodes {
		if status.LastSync == nil || node.RecordedAt.After(status.LastSync.CompletedAt) {
			result.ChangedNodesSinceSync = appendBoundedNode(result.ChangedNodesSinceSync, node, limit, &result.Truncated)
		}
		switch node.Status {
		case "active", "accepted":
			result.CurrentFocus = appendBoundedNode(result.CurrentFocus, node, limit, &result.Truncated)
		case "blocked":
			result.Blocked = appendBoundedNode(result.Blocked, node, limit, &result.Truncated)
		case "proposed", "deferred":
			result.Future = appendBoundedNode(result.Future, node, limit, &result.Truncated)
		case "completed":
			result.Completed = appendBoundedNode(result.Completed, node, limit, &result.Truncated)
		}
	}
	links, linksTruncated, err := service.currentNodeLinks(ctx, projectID, limit)
	if err != nil {
		return Overview{}, err
	}
	result.Relationships, result.Truncated = links, result.Truncated || linksTruncated
	result.Tracks, linksTruncated = roadmapTracks(nodes, links, limit)
	result.Truncated = result.Truncated || linksTruncated
	return result, nil
}

func roadmapTracks(nodes []Node, links []NodeLink, limit int) ([]Track, bool) {
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	count := len(nodes)
	if count > limit {
		count = limit
	}
	tracks := make([]Track, 0, count)
	index := make(map[string]int, count)
	for _, node := range nodes[:count] {
		index[node.ID] = len(tracks)
		tracks = append(tracks, emptyTrack(nodeRef(node)))
	}
	for _, link := range links {
		source, sourceOK := byID[link.NodeID]
		target, targetOK := byID[link.TargetNodeID]
		if !sourceOK || !targetOK {
			continue
		}
		sourceRef, targetRef := nodeRef(source), nodeRef(target)
		sourceIndex, sourceVisible := index[source.ID]
		targetIndex, targetVisible := index[target.ID]
		switch link.Relationship {
		case "parent_of":
			if sourceVisible {
				tracks[sourceIndex].Subtasks = appendNodeRef(tracks[sourceIndex].Subtasks, targetRef)
			}
			if targetVisible {
				tracks[targetIndex].Parents = appendNodeRef(tracks[targetIndex].Parents, sourceRef)
			}
		case "depends_on":
			if sourceVisible {
				tracks[sourceIndex].DependsOn = appendNodeRef(tracks[sourceIndex].DependsOn, targetRef)
				if target.Status != "completed" {
					tracks[sourceIndex].UnresolvedDependencies = appendNodeRef(tracks[sourceIndex].UnresolvedDependencies, targetRef)
				}
			}
		case "blocks":
			if sourceVisible {
				tracks[sourceIndex].Blocks = appendNodeRef(tracks[sourceIndex].Blocks, targetRef)
			}
			if targetVisible {
				tracks[targetIndex].BlockedBy = appendNodeRef(tracks[targetIndex].BlockedBy, sourceRef)
			}
		case "parallel_with":
			if sourceVisible {
				tracks[sourceIndex].ParallelWith = appendNodeRef(tracks[sourceIndex].ParallelWith, targetRef)
			}
			if targetVisible {
				tracks[targetIndex].ParallelWith = appendNodeRef(tracks[targetIndex].ParallelWith, sourceRef)
			}
		case "explores":
			if sourceVisible {
				tracks[sourceIndex].Explores = appendNodeRef(tracks[sourceIndex].Explores, targetRef)
			}
		}
	}
	for i := range tracks {
		sortTrack(&tracks[i])
	}
	return tracks, len(nodes) > limit
}

func emptyTrack(node NodeRef) Track {
	return Track{Node: node, Parents: []NodeRef{}, Subtasks: []NodeRef{}, DependsOn: []NodeRef{}, UnresolvedDependencies: []NodeRef{}, BlockedBy: []NodeRef{}, Blocks: []NodeRef{}, ParallelWith: []NodeRef{}, Explores: []NodeRef{}}
}

func nodeRef(node Node) NodeRef {
	return NodeRef{ID: node.ID, Title: node.Title, Status: node.Status, Priority: node.Priority, Kind: node.Kind}
}

func appendNodeRef(values []NodeRef, value NodeRef) []NodeRef {
	for _, existing := range values {
		if existing.ID == value.ID {
			return values
		}
	}
	return append(values, value)
}

func sortTrack(track *Track) {
	less := func(values []NodeRef) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Priority != values[j].Priority {
				return values[i].Priority < values[j].Priority
			}
			return values[i].ID < values[j].ID
		})
	}
	less(track.Parents)
	less(track.Subtasks)
	less(track.DependsOn)
	less(track.UnresolvedDependencies)
	less(track.BlockedBy)
	less(track.Blocks)
	less(track.ParallelWith)
	less(track.Explores)
}

func reviewTriggers(status Status) []ReviewTrigger {
	if status.ReviewRoutine == nil {
		return []ReviewTrigger{}
	}
	routine := status.ReviewRoutine
	result := []ReviewTrigger{}
	if status.LastSync == nil {
		result = append(result, ReviewTrigger{Kind: "model_sync", State: "due", RoutineID: routine.ID, RoutineRevision: routine.Revision})
	}
	add := func(kind string, threshold, observed *int64, dueReason string) {
		if threshold == nil {
			return
		}
		state := "not_evaluated"
		if observed != nil {
			state = "pending"
			if contains(status.DueReasons, dueReason) {
				state = "due"
			}
		}
		result = append(result, ReviewTrigger{Kind: kind, Threshold: threshold, Observed: observed, State: state, RoutineID: routine.ID, RoutineRevision: routine.Revision})
	}
	if routine.CommitThreshold != nil {
		threshold := int64(*routine.CommitThreshold)
		var observed *int64
		if status.CommitsSinceSync != nil {
			value := int64(*status.CommitsSinceSync)
			observed = &value
		}
		add("commits", &threshold, observed, "commit_threshold")
	}
	add("elapsed_seconds", routine.ElapsedSecondsThreshold, status.ElapsedSeconds, "elapsed_time_threshold")
	if routine.ChangedFilesThreshold != nil {
		threshold := int64(*routine.ChangedFilesThreshold)
		var observed *int64
		if status.LastSync != nil {
			value := int64(len(status.ChangedFiles))
			observed = &value
		}
		add("changed_files", &threshold, observed, "changed_files_threshold")
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func appendBoundedNode(nodes []Node, node Node, limit int, truncated *bool) []Node {
	if len(nodes) >= limit {
		*truncated = true
		return nodes
	}
	return append(nodes, node)
}

func (service Service) currentNodeLinks(ctx context.Context, projectID string, limit int) ([]NodeLink, bool, error) {
	return currentNodeLinksFrom(ctx, service.DB, projectID, limit)
}

func currentNodeLinksFrom(ctx context.Context, db roadmapQueryer, projectID string, limit int) ([]NodeLink, bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT l.id, l.node_id, l.node_revision, l.target_node_id, l.relationship, l.authority, l.recorded_at, l.schema_version
		FROM roadmap_node_links l
		JOIN roadmap_nodes n ON n.project_id = l.project_id AND n.node_id = l.node_id AND n.revision = l.node_revision
		WHERE l.project_id = ?
		AND n.revision = (SELECT MAX(latest.revision) FROM roadmap_nodes latest WHERE latest.project_id = n.project_id AND latest.node_id = n.node_id)
		AND n.status != 'superseded'
		ORDER BY n.priority, l.node_id, l.relationship, l.target_node_id, l.recorded_at LIMIT ?`, projectID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	links := []NodeLink{}
	for rows.Next() {
		var link NodeLink
		var recorded string
		if err := rows.Scan(&link.ID, &link.NodeID, &link.NodeRevision, &link.TargetNodeID, &link.Relationship, &link.Authority, &recorded, &link.SchemaVersion); err != nil {
			return nil, false, err
		}
		link.ProjectID = projectID
		if link.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, false, err
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return nil, false, err
	}
	if len(links) > limit {
		return links[:limit], true, nil
	}
	return links, false, nil
}
