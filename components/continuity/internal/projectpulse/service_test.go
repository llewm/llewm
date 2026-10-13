package projectpulse

import (
	"context"
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
)

type fakeCheckpoints struct{ latest *checkpoint.Checkpoint }

func (fake fakeCheckpoints) LatestSelected(context.Context, string, checkpoint.Selection) (*checkpoint.Checkpoint, error) {
	return fake.latest, nil
}

type fakeRoadmap struct {
	status    roadmap.Status
	nodes     []roadmap.Node
	inventory roadmap.TrackInventory
}

func (fake fakeRoadmap) Status(context.Context, string) (roadmap.Status, error) {
	return fake.status, nil
}

func (fake fakeRoadmap) CurrentNodes(context.Context, string) ([]roadmap.Node, error) {
	return fake.nodes, nil
}

func (fake fakeRoadmap) TrackInventory(context.Context, string, roadmap.TrackInventoryOptions) (roadmap.TrackInventory, error) {
	return fake.inventory, nil
}

func TestNextUsesCheckpointRecommendationAndLabelsProvenance(t *testing.T) {
	service := Service{
		Checkpoints: fakeCheckpoints{latest: &checkpoint.Checkpoint{ID: "checkpoint", TaskID: "task", Task: "Current task", Status: "in_progress", NextAction: "Run the focused check", Authority: "caller_asserted"}},
		Roadmap: fakeRoadmap{
			status:    roadmap.Status{Freshness: "changed"},
			nodes:     []roadmap.Node{{ID: "first", Priority: 10, Authority: "caller_asserted"}},
			inventory: roadmap.TrackInventory{Total: 1, Items: []roadmap.TrackInventoryItem{{SelectedTaskID: "task", LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "checkpoint"}}}},
		},
	}
	pulse, err := service.Next(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if pulse.RecommendedNext != "Run the focused check" || pulse.RecommendationSource != "project_latest_checkpoint.next_action" || pulse.RoutingDecision != "project_latest" || pulse.CheckpointAuthority != "caller_asserted" || pulse.RepositoryAuthority != "machine_observed" || pulse.RoadmapAuthority != "caller_asserted" || pulse.RoadmapStatus.Freshness != "changed" {
		t.Fatalf("unexpected pulse: %#v", pulse)
	}
}

func TestNextWithoutCheckpointIsHonestAndBoundsNodes(t *testing.T) {
	nodes := make([]roadmap.Node, 21)
	for index := range nodes {
		nodes[index] = roadmap.Node{ID: fmt.Sprintf("node-%02d", index)}
	}
	service := Service{Checkpoints: fakeCheckpoints{}, Roadmap: fakeRoadmap{status: roadmap.Status{Freshness: "no_sync"}, nodes: nodes}}
	pulse, err := service.Next(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if pulse.Checkpoint != nil || pulse.RecommendedNext != "" || pulse.RecommendationSource != "" || pulse.RoutingDecision != "no_candidate" || len(pulse.RoadmapNodes) != 20 || !pulse.RoadmapNodesTruncated {
		t.Fatalf("unexpected empty-checkpoint pulse: %#v", pulse)
	}
}

func TestNextHonorsExplicitSelectionEvenWhenCompleted(t *testing.T) {
	service := Service{
		Checkpoints: fakeCheckpoints{latest: &checkpoint.Checkpoint{ID: "selected", TaskID: "selected-task", Status: "completed", NextAction: "Task complete", Authority: "caller_asserted"}},
		Roadmap:     fakeRoadmap{},
	}
	pulse, err := service.NextSelected(context.Background(), "project", checkpoint.Selection{TaskID: "selected-task"})
	if err != nil {
		t.Fatal(err)
	}
	if pulse.RoutingDecision != "explicit_selection" || pulse.RecommendedNext != "Task complete" || pulse.RecommendationSource != "explicit_checkpoint.next_action" || pulse.SelectionRequired {
		t.Fatalf("explicit selection was not honored: %#v", pulse)
	}
}

func TestNextRecommendsOnlyResumableTrack(t *testing.T) {
	item := roadmap.TrackInventoryItem{Node: &roadmap.NodeRef{ID: "resume", Title: "Resume work", Status: "accepted", Priority: 12}, NodeRevision: 3, SelectedTaskID: "resume-task", LatestCheckpoint: &roadmap.TrackCheckpoint{ID: "resume-checkpoint", Status: "paused", NextAction: "Continue exact work"}, Resumable: true, ResumableReasons: []string{"explicit_return_candidate"}}
	service := Service{
		Checkpoints: fakeCheckpoints{latest: &checkpoint.Checkpoint{ID: "done", TaskID: "done-task", Status: "completed", NextAction: "Task complete"}},
		Roadmap:     fakeRoadmap{inventory: roadmap.TrackInventory{Total: 1, Items: []roadmap.TrackInventoryItem{item}}},
	}
	pulse, err := service.Next(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if pulse.RoutingDecision != "single_candidate" || pulse.RecommendedTrack == nil || pulse.RecommendedTrack.RoadmapNodeID != "resume" || pulse.RecommendedNext != "Continue exact work" || pulse.RecommendationSource != "single_resumable_track.checkpoint.next_action" {
		t.Fatalf("single candidate was not recommended: %#v", pulse)
	}
}

func TestNextRequiresSelectionForSeveralResumableTracks(t *testing.T) {
	items := []roadmap.TrackInventoryItem{
		{Node: &roadmap.NodeRef{ID: "a", Title: "A", Status: "accepted", Priority: 1}, Resumable: true, ResumableReasons: []string{"accepted_roadmap_work_without_task"}},
		{Node: &roadmap.NodeRef{ID: "b", Title: "B", Status: "accepted", Priority: 2}, Resumable: true, ResumableReasons: []string{"accepted_roadmap_work_without_task"}},
	}
	service := Service{Checkpoints: fakeCheckpoints{latest: &checkpoint.Checkpoint{ID: "done", Status: "completed"}}, Roadmap: fakeRoadmap{inventory: roadmap.TrackInventory{Total: 2, Items: items}}}
	pulse, err := service.Next(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if pulse.RoutingDecision != "selection_required" || !pulse.SelectionRequired || pulse.RecommendedNext != "" || len(pulse.RoutingCandidates) != 2 {
		t.Fatalf("ambiguous tracks were ranked or hidden: %#v", pulse)
	}
}
