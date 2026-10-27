package workstate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/projectpulse"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

type pulseReader interface {
	Next(context.Context, string) (projectpulse.Pulse, error)
}

type inventoryReader interface {
	TrackInventory(context.Context, string, roadmap.TrackInventoryOptions) (roadmap.TrackInventory, error)
}

type planReader interface {
	Ready(context.Context, string, trackplan.PlanListOptions) ([]trackplan.ReadyItem, error)
}

type Service struct {
	Root    string
	Pulse   pulseReader
	Roadmap inventoryReader
	Plans   planReader
}

func (service Service) Summary(ctx context.Context, projectID string, limit int) (Summary, error) {
	limit, err := boundedLimit(limit)
	if err != nil {
		return Summary{}, err
	}
	pulse, err := service.Pulse.Next(ctx, projectID)
	if err != nil {
		return Summary{}, err
	}
	inventory, err := service.Roadmap.TrackInventory(ctx, projectID, roadmap.TrackInventoryOptions{Filter: "current_focus", Limit: MaxLimit, RefLimit: 5})
	if err != nil {
		return Summary{}, err
	}
	ready, err := service.Plans.Ready(ctx, projectID, trackplan.PlanListOptions{Limit: MaxLimit})
	if err != nil {
		return Summary{}, err
	}
	readyCount := len(ready)
	if len(ready) > limit {
		ready = ready[:limit]
	}
	allItems := inventory.Items
	allFindings := findingsFor(allItems)
	items := allItems
	if len(items) > limit {
		items = items[:limit]
	}
	findings := allFindings
	findingsTruncated := false
	if len(findings) > limit {
		findings, findingsTruncated = findings[:limit], true
	}
	result := Summary{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Checkpoint: pulse.Checkpoint,
		RecommendedNext: pulse.RecommendedNext, RoutingDecision: pulse.RoutingDecision,
		RoadmapFreshness: pulse.RoadmapStatus.Freshness, RoadmapReviewDue: pulse.RoadmapStatus.ReviewDue,
		CurrentWork: items, ReadyItems: ready, Findings: findings,
		InventoryTruncated: inventory.Truncated || len(allItems) > len(items), FindingsTruncated: findingsTruncated,
		Authority:  "mixed",
		Disclaimer: "Current work combines caller-asserted roadmap, checkpoint, binding, work item, and plan state with deterministic joins, counts, findings, repository freshness, and routing. It does not select, schedule, repair, delegate, or execute work.",
	}
	result.Counts.CurrentFocus = inventory.Total
	result.Counts.Ready = readyCount
	for _, item := range allItems {
		if item.Active {
			result.Counts.Active++
		}
		if item.Blocked {
			result.Counts.Blocked++
		}
		if item.Resumable {
			result.Counts.Resumable++
		}
		if len(item.Anomalies) > 0 {
			result.Counts.Anomalous++
		}
	}
	return result, nil
}

func (service Service) Check(ctx context.Context, projectID, scope string, limit int) (Check, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "current"
	}
	filter := "current_focus"
	if scope == "all" {
		filter = "all"
	} else if scope != "current" {
		return Check{}, fmt.Errorf("state-check scope must be current or all")
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return Check{}, err
	}
	inventory, err := service.Roadmap.TrackInventory(ctx, projectID, roadmap.TrackInventoryOptions{Filter: filter, Limit: MaxLimit, RefLimit: 5})
	if err != nil {
		return Check{}, err
	}
	findings := findingsFor(inventory.Items)
	total := len(findings)
	findingsTruncated := false
	if len(findings) > limit {
		findings, findingsTruncated = findings[:limit], true
	}
	state := "current"
	if total > 0 {
		state = "findings"
	}
	return Check{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Scope: scope, State: state,
		Findings: findings, Total: total, Truncated: inventory.Truncated || findingsTruncated,
		Authority:  "machine_observed",
		Disclaimer: "Findings are deterministic comparisons over saved records and bounded project files. They do not infer intent, repair state, change task selection, or authorize work.",
	}, nil
}

func (service Service) List(ctx context.Context, projectID, view string, limit int) (List, error) {
	view = strings.TrimSpace(view)
	if view == "" {
		view = "current"
	}
	filters := map[string]string{
		"open":    "open",
		"current": "current_focus",
		"active":  "active",
		"next":    "resumable",
		"blocked": "blocked",
		"stale":   "stale",
		"actions": "actions",
		"all":     "all",
		// everything also lists finished tasks that have no roadmap node.
		"everything": "everything",
	}
	filter, ok := filters[view]
	if !ok {
		return List{}, fmt.Errorf("work view must be open, current, active, next, blocked, stale, actions, all, or everything")
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return List{}, err
	}
	inventory, err := service.Roadmap.TrackInventory(ctx, projectID, roadmap.TrackInventoryOptions{Filter: filter, Limit: limit, RefLimit: 5})
	if err != nil {
		return List{}, err
	}
	result := List{
		SchemaVersion: SchemaVersion, ProjectID: projectID, View: view, Items: inventory.Items,
		ReadyItems: []trackplan.ReadyItem{}, InventoryTotal: inventory.Total, InventoryOmitted: inventory.Omitted,
		Truncated: inventory.Truncated, Authority: "mixed",
		Disclaimer: "Work views are deterministic projections of saved roadmap, task, checkpoint, work item, task transition, and plan records. They do not select, schedule, repair, delegate, or execute work.",
	}
	if view == "next" {
		ready, err := service.Plans.Ready(ctx, projectID, trackplan.PlanListOptions{Limit: MaxLimit})
		if err != nil {
			return List{}, err
		}
		result.ReadyTotal = len(ready)
		if len(ready) > limit {
			result.ReadyItems = ready[:limit]
			result.ReadyOmitted = len(ready) - limit
			result.Truncated = true
		} else {
			result.ReadyItems = ready
		}
	}
	return result, nil
}

func (service Service) Show(ctx context.Context, projectID, identifier string) (Detail, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return Detail{}, fmt.Errorf("work identifier is required")
	}
	if len(identifier) > 205 {
		return Detail{}, fmt.Errorf("work identifier must not exceed 205 bytes")
	}
	type candidate struct {
		item      roadmap.TrackInventoryItem
		matchedBy string
	}
	candidates := []candidate{}
	query := func(options roadmap.TrackInventoryOptions, matchedBy string) error {
		inventory, err := service.Roadmap.TrackInventory(ctx, projectID, options)
		if err != nil {
			return err
		}
		for _, item := range inventory.Items {
			duplicate := false
			for index := range candidates {
				if sameInventoryItem(candidates[index].item, item) {
					duplicate = true
					if candidates[index].matchedBy != matchedBy {
						candidates[index].matchedBy = "roadmap_node_and_task"
					}
					break
				}
			}
			if !duplicate {
				candidates = append(candidates, candidate{item: item, matchedBy: matchedBy})
			}
		}
		return nil
	}
	switch {
	case strings.HasPrefix(identifier, "node:"):
		nodeID := strings.TrimPrefix(identifier, "node:")
		if nodeID == "" {
			return Detail{}, fmt.Errorf("roadmap node ID is required after node:")
		}
		if err := query(roadmap.TrackInventoryOptions{Filter: "all", RoadmapNodeID: nodeID, Limit: 2, RefLimit: 10}, "roadmap_node"); err != nil {
			return Detail{}, err
		}
	case strings.HasPrefix(identifier, "task:"):
		taskID := strings.TrimPrefix(identifier, "task:")
		if taskID == "" {
			return Detail{}, fmt.Errorf("task ID is required after task:")
		}
		if err := query(roadmap.TrackInventoryOptions{Filter: "all", TaskID: taskID, Limit: 2, RefLimit: 10}, "task"); err != nil {
			return Detail{}, err
		}
	default:
		if err := query(roadmap.TrackInventoryOptions{Filter: "all", RoadmapNodeID: identifier, Limit: 2, RefLimit: 10}, "roadmap_node"); err != nil {
			return Detail{}, err
		}
		if err := query(roadmap.TrackInventoryOptions{Filter: "all", TaskID: identifier, Limit: 2, RefLimit: 10}, "task"); err != nil {
			return Detail{}, err
		}
	}
	if len(candidates) == 0 {
		return Detail{}, fmt.Errorf("work %q not found in this project", identifier)
	}
	if len(candidates) > 1 {
		return Detail{}, fmt.Errorf("work identifier %q is ambiguous; use node:<id> or task:<id>", identifier)
	}
	return Detail{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Identifier: identifier,
		MatchedBy: candidates[0].matchedBy, Item: candidates[0].item, Authority: "mixed",
		Disclaimer: "This is an exact read-only joined view of saved records. It does not select, resume, repair, delegate, or execute work.",
	}, nil
}

func sameInventoryItem(left, right roadmap.TrackInventoryItem) bool {
	leftNode, rightNode := "", ""
	if left.Node != nil {
		leftNode = left.Node.ID
	}
	if right.Node != nil {
		rightNode = right.Node.ID
	}
	return leftNode == rightNode && left.SelectedTaskID == right.SelectedTaskID && left.BindingState == right.BindingState
}

func boundedLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < 1 || limit > MaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	}
	return limit, nil
}

func findingsFor(items []roadmap.TrackInventoryItem) []Finding {
	findings := []Finding{}
	for _, item := range items {
		for _, code := range item.Anomalies {
			findings = append(findings, findingFor(item, code))
		}
		if item.Node != nil && item.Node.Status == "active" && item.SelectedTaskID != "" && item.LatestCheckpoint != nil && item.LatestCheckpoint.Status == "completed" {
			findings = append(findings, findingFor(item, "roadmap_terminal_task"))
		}
		if item.LatestCheckpoint != nil && item.LatestCheckpoint.Status == "completed" && item.UnresolvedActionCount > 0 {
			findings = append(findings, findingFor(item, "terminal_task_open_action"))
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].RoadmapNode != findings[j].RoadmapNode {
			return findings[i].RoadmapNode < findings[j].RoadmapNode
		}
		if findings[i].TaskID != findings[j].TaskID {
			return findings[i].TaskID < findings[j].TaskID
		}
		return findings[i].Code < findings[j].Code
	})
	return findings
}

func findingFor(item roadmap.TrackInventoryItem, code string) Finding {
	result := Finding{Code: code, Severity: "warning", TaskID: item.SelectedTaskID, SourceRefs: []string{}}
	if code == "roadmap_checkpoint_status" {
		result.Severity = "info"
	}
	if item.Node != nil {
		result.RoadmapNode = item.Node.ID
		result.SourceRefs = append(result.SourceRefs, fmt.Sprintf("roadmap:%s@%d", item.Node.ID, item.NodeRevision))
	}
	if result.TaskID != "" {
		result.SourceRefs = append(result.SourceRefs, "task:"+result.TaskID)
	}
	if item.LatestCheckpoint != nil {
		result.CheckpointID = item.LatestCheckpoint.ID
		result.SourceRefs = append(result.SourceRefs, "checkpoint:"+item.LatestCheckpoint.ID)
	}
	if code == "roadmap_checkpoint_status" && item.Node != nil && item.LatestCheckpoint != nil {
		result.Summary = fmt.Sprintf("Recorded roadmap status %q differs from selected checkpoint status %q; this comparison does not determine that either record is stale or should be changed.", item.Node.Status, item.LatestCheckpoint.Status)
	} else {
		result.Summary = findingSummary(code)
	}
	return result
}

// FindingSummary returns the documented summary for a finding code.
func FindingSummary(code string) string { return findingSummary(code) }

func findingSummary(code string) string {
	summaries := map[string]string{
		"ambiguous_task_binding":    "Several task identities are active for the same roadmap node.",
		"missing_checkpoint":        "The bound task has no recorded checkpoint.",
		"missing_task_binding":      "The active or blocked roadmap node has no recorded task binding or same-ID task.",
		"missing_track_plan":        "The roadmap node has an explicit task binding but no track plan; propose one from its contract and have it reviewed.",
		"track_plan_behind":         "The roadmap node status and its latest track plan disagree: a completed node has unfinished plan items, or a finished plan belongs to an unfinished node.",
		"track_plan_unreviewed":     "The bound roadmap node's track plans are all still proposed; record the review with llewm track-plan record PLAN --creator NAME.",
		"multiple_tip":              "The task has several tips in its strict predecessor history, including legacy checkpoints; use fork list to check for unresolved current heads.",
		"orphan":                    "The task has several roots in its strict predecessor history, including legacy checkpoints; this does not by itself establish a current fork.",
		"roadmap_checkpoint_status": "The recorded roadmap and selected checkpoint statuses differ.",
		"roadmap_terminal_task":     "An active roadmap node has a selected task whose latest checkpoint is completed.",
		"stale_task_binding":        "The active task binding references an older roadmap-node revision.",
		"task_without_roadmap_node": "The recorded task is not associated with a roadmap node; bind it with llewm roadmap link --kind task.",
		"work_on_inactive_node":     "Work is underway (an open task or an accepted or active plan) on a roadmap node that is still deferred or proposed; record the node as active.",
		"terminal_selection":        "Managed lifecycle state and the selected checkpoint terminal state disagree.",
		"terminal_task_open_action": "A completed checkpoint still has an unresolved checkpoint action.",
	}
	if summary := summaries[code]; summary != "" {
		return summary
	}
	return "Saved project-state records contain anomaly " + code + "."
}
