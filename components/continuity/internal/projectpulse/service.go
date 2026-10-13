package projectpulse

import (
	"context"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

const (
	SchemaVersion        = 2
	maxNodes             = 20
	maxRoutingCandidates = 10
)

type checkpointReader interface {
	LatestSelected(context.Context, string, checkpoint.Selection) (*checkpoint.Checkpoint, error)
}

type roadmapReader interface {
	Status(context.Context, string) (roadmap.Status, error)
	CurrentNodes(context.Context, string) ([]roadmap.Node, error)
	TrackInventory(context.Context, string, roadmap.TrackInventoryOptions) (roadmap.TrackInventory, error)
}

type Service struct {
	Checkpoints checkpointReader
	Roadmap     roadmapReader
}

type Pulse struct {
	SchemaVersion         int                    `json:"schema_version"`
	ProjectID             string                 `json:"project_id"`
	Checkpoint            *checkpoint.Checkpoint `json:"checkpoint,omitempty"`
	RoadmapStatus         roadmap.Status         `json:"roadmap_status"`
	RoadmapNodes          []roadmap.Node         `json:"roadmap_nodes"`
	RoadmapNodesTruncated bool                   `json:"roadmap_nodes_truncated"`
	RecommendedNext       string                 `json:"recommended_next,omitempty"`
	RecommendationSource  string                 `json:"recommendation_source,omitempty"`
	RoutingDecision       string                 `json:"routing_decision"`
	RoutingRule           string                 `json:"routing_rule"`
	SelectionRequired     bool                   `json:"selection_required"`
	RecommendedTrack      *RoutingCandidate      `json:"recommended_track,omitempty"`
	RoutingCandidates     []RoutingCandidate     `json:"routing_candidates"`
	RoutingCandidateTotal int                    `json:"routing_candidate_total"`
	RoutingTruncated      bool                   `json:"routing_truncated"`
	RoutingAuthority      string                 `json:"routing_authority"`
	CheckpointAuthority   string                 `json:"checkpoint_authority,omitempty"`
	RoadmapAuthority      string                 `json:"roadmap_authority"`
	RepositoryAuthority   string                 `json:"repository_authority"`
	Disclaimer            string                 `json:"disclaimer"`
}

type RoutingCandidate struct {
	RoadmapNodeID    string   `json:"roadmap_node_id,omitempty"`
	RoadmapRevision  int      `json:"roadmap_revision,omitempty"`
	Title            string   `json:"title"`
	RoadmapStatus    string   `json:"roadmap_status,omitempty"`
	Priority         int      `json:"priority,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	CheckpointID     string   `json:"checkpoint_id,omitempty"`
	CheckpointStatus string   `json:"checkpoint_status,omitempty"`
	NextAction       string   `json:"next_action,omitempty"`
	ResumableReasons []string `json:"resumable_reasons"`
	Anomalies        []string `json:"anomalies"`
}

func (service Service) Next(ctx context.Context, projectID string) (Pulse, error) {
	return service.NextSelected(ctx, projectID, checkpoint.Selection{})
}

func (service Service) NextSelected(ctx context.Context, projectID string, selection checkpoint.Selection) (Pulse, error) {
	selection.CheckpointID = strings.TrimSpace(selection.CheckpointID)
	selection.TaskID = strings.TrimSpace(selection.TaskID)
	selection.SessionID = strings.TrimSpace(selection.SessionID)
	latest, err := service.Checkpoints.LatestSelected(ctx, projectID, selection)
	if err != nil {
		return Pulse{}, err
	}
	status, err := service.Roadmap.Status(ctx, projectID)
	if err != nil {
		return Pulse{}, err
	}
	nodes, err := service.Roadmap.CurrentNodes(ctx, projectID)
	if err != nil {
		return Pulse{}, err
	}
	result := Pulse{
		SchemaVersion:       SchemaVersion,
		ProjectID:           projectID,
		Checkpoint:          latest,
		RoadmapStatus:       status,
		RoadmapNodes:        nodes,
		RoadmapAuthority:    "caller_asserted",
		RoutingCandidates:   []RoutingCandidate{},
		RoutingAuthority:    "mixed",
		RepositoryAuthority: "machine_observed",
		Disclaimer:          "From Continuity saved state and deterministic repository observations; no model was invoked and current files were not reevaluated for meaning. Routing is read-only structural visibility, not task selection, scheduling, or authority to execute work.",
	}
	if len(result.RoadmapNodes) > maxNodes {
		result.RoadmapNodes = result.RoadmapNodes[:maxNodes]
		result.RoadmapNodesTruncated = true
	}
	if hasSelection(selection) {
		if latest == nil {
			result.RoutingDecision = "explicit_selection_missing"
			result.RoutingRule = "explicit_selection_no_match"
			return result, nil
		}
		result.RecommendedNext = latest.NextAction
		result.RecommendationSource = "explicit_checkpoint.next_action"
		result.CheckpointAuthority = latest.Authority
		result.RoutingDecision = "explicit_selection"
		result.RoutingRule = "caller_selected_checkpoint_or_task"
		return result, nil
	}

	if latest != nil && latest.Status != "completed" {
		currentItems := []roadmap.TrackInventoryItem{}
		if latest.TaskID != "" {
			current, err := service.Roadmap.TrackInventory(ctx, projectID, roadmap.TrackInventoryOptions{Filter: "all", TaskID: latest.TaskID, Limit: 2, RefLimit: 1})
			if err != nil {
				return Pulse{}, err
			}
			currentItems = current.Items
		}
		if structurallyCurrent(latest, currentItems) {
			result.RecommendedNext = latest.NextAction
			result.RecommendationSource = "project_latest_checkpoint.next_action"
			result.CheckpointAuthority = latest.Authority
			result.RoutingDecision = "project_latest"
			result.RoutingRule = "nonterminal_structurally_current_project_tip"
			return result, nil
		}
	}
	inventory, err := service.Roadmap.TrackInventory(ctx, projectID, roadmap.TrackInventoryOptions{Filter: "resumable", Limit: maxRoutingCandidates + 1, RefLimit: 1})
	if err != nil {
		return Pulse{}, err
	}
	result.RoutingCandidateTotal = inventory.Total
	for _, item := range inventory.Items {
		if len(result.RoutingCandidates) == maxRoutingCandidates {
			break
		}
		result.RoutingCandidates = append(result.RoutingCandidates, routingCandidate(item))
	}
	result.RoutingTruncated = inventory.Total > len(result.RoutingCandidates) || inventory.Truncated
	switch inventory.Total {
	case 0:
		result.RoutingDecision = "no_candidate"
		result.RoutingRule = "no_resumable_track_recorded"
	case 1:
		candidate := result.RoutingCandidates[0]
		result.RecommendedTrack = &candidate
		result.RoutingDecision = "single_candidate"
		result.RoutingRule = "only_resumable_track"
		if candidate.NextAction != "" {
			result.RecommendedNext = candidate.NextAction
			result.RecommendationSource = "single_resumable_track.checkpoint.next_action"
		} else if candidate.RoadmapNodeID != "" {
			result.RecommendedNext = "Select track " + candidate.RoadmapNodeID + ": " + candidate.Title
			result.RecommendationSource = "single_resumable_track.roadmap_node"
		}
	default:
		result.RoutingDecision = "selection_required"
		result.RoutingRule = "multiple_resumable_tracks"
		result.SelectionRequired = true
	}
	return result, nil
}

func hasSelection(selection checkpoint.Selection) bool {
	return selection.CheckpointID != "" || selection.TaskID != "" || selection.SessionID != ""
}

func structurallyCurrent(latest *checkpoint.Checkpoint, items []roadmap.TrackInventoryItem) bool {
	if latest == nil {
		return false
	}
	// Legacy taskless checkpoints remain the project-latest compatibility surface.
	// They cannot participate in task-scoped inventory until explicitly reconciled.
	if latest.TaskID == "" {
		return true
	}
	for _, item := range items {
		if item.SelectedTaskID != latest.TaskID || item.LatestCheckpoint == nil || item.LatestCheckpoint.ID != latest.ID {
			continue
		}
		for _, anomaly := range item.Anomalies {
			if anomaly == "ambiguous_task_binding" || anomaly == "multiple_tip" || anomaly == "terminal_selection" {
				return false
			}
		}
		return true
	}
	return false
}

func routingCandidate(item roadmap.TrackInventoryItem) RoutingCandidate {
	result := RoutingCandidate{TaskID: item.SelectedTaskID, ResumableReasons: append([]string{}, item.ResumableReasons...), Anomalies: append([]string{}, item.Anomalies...)}
	if item.Node != nil {
		result.RoadmapNodeID, result.RoadmapRevision, result.Title, result.RoadmapStatus, result.Priority = item.Node.ID, item.NodeRevision, item.Node.Title, item.Node.Status, item.Node.Priority
	}
	if item.LatestCheckpoint != nil {
		result.CheckpointID, result.CheckpointStatus, result.NextAction = item.LatestCheckpoint.ID, item.LatestCheckpoint.Status, item.LatestCheckpoint.NextAction
		if result.Title == "" {
			result.Title = item.SelectedTaskID
		}
	}
	return result
}
