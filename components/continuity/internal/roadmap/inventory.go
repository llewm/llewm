package roadmap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

const (
	DefaultTrackInventoryLimit    = 20
	MaxTrackInventoryLimit        = 100
	DefaultTrackInventoryRefLimit = 10
	MaxTrackInventoryRefLimit     = 50
	maxTrackInventoryTasks        = 2000
	maxTrackInventoryLinks        = 1000
)

// AppendTaskBinding records one immutable binding assertion. A later revision
// for the same node/task pair supersedes only that pair; it never silently
// resolves another active pair for the node.
func (service Service) AppendTaskBinding(ctx context.Context, value TaskBinding) (TaskBinding, error) {
	var err error
	for name, field := range map[string]*string{
		"project ID": &value.ProjectID, "roadmap node ID": &value.RoadmapNodeID,
		"task ID": &value.TaskID, "status": &value.Status, "reason": &value.Reason,
		"request key": &value.RequestKey,
	} {
		if *field, err = text(name, *field, name != "reason"); err != nil {
			return TaskBinding{}, err
		}
	}
	if !oneOf(value.Status, "active", "superseded") {
		return TaskBinding{}, fmt.Errorf("task binding status must be active or superseded")
	}
	if value.RoadmapNodeRevision == 0 {
		if err := service.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM roadmap_nodes WHERE project_id=? AND node_id=?`, value.ProjectID, value.RoadmapNodeID).Scan(&value.RoadmapNodeRevision); err != nil {
			return TaskBinding{}, err
		}
	}
	var nodeCount, taskCount int
	if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_nodes WHERE project_id=? AND node_id=? AND revision=?`, value.ProjectID, value.RoadmapNodeID, value.RoadmapNodeRevision).Scan(&nodeCount); err != nil {
		return TaskBinding{}, err
	}
	if nodeCount == 0 {
		return TaskBinding{}, fmt.Errorf("roadmap node %q revision %d is not recorded", value.RoadmapNodeID, value.RoadmapNodeRevision)
	}
	if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT task_id FROM checkpoints WHERE project_id=? AND task_id=?
		UNION SELECT task_id FROM task_lineage_events WHERE project_id=? AND task_id=?
		UNION SELECT target_task_id FROM task_lineage_events WHERE project_id=? AND target_task_id=?
	)`, value.ProjectID, value.TaskID, value.ProjectID, value.TaskID, value.ProjectID, value.TaskID).Scan(&taskCount); err != nil {
		return TaskBinding{}, err
	}
	if taskCount == 0 {
		return TaskBinding{}, fmt.Errorf("task %q is not recorded in checkpoint or managed lifecycle state", value.TaskID)
	}
	requestedRevision := value.Revision
	if value.Revision == 0 {
		if err := service.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM roadmap_task_bindings WHERE project_id=? AND roadmap_node_id=? AND task_id=?`, value.ProjectID, value.RoadmapNodeID, value.TaskID).Scan(&value.Revision); err != nil {
			return TaskBinding{}, err
		}
	}
	if value.Revision < 1 {
		return TaskBinding{}, fmt.Errorf("binding revision must be positive")
	}
	requestHash, err := taskBindingHash(value)
	if err != nil {
		return TaskBinding{}, err
	}
	var existing TaskBinding
	var existingHash, recorded string
	err = service.DB.QueryRowContext(ctx, `SELECT roadmap_node_id,task_id,revision,roadmap_node_revision,status,reason,request_key,request_sha256,authority,recorded_at,schema_version FROM roadmap_task_bindings WHERE project_id=? AND request_key=?`, value.ProjectID, value.RequestKey).Scan(
		&existing.RoadmapNodeID, &existing.TaskID, &existing.Revision, &existing.RoadmapNodeRevision, &existing.Status, &existing.Reason, &existing.RequestKey, &existingHash, &existing.Authority, &recorded, &existing.SchemaVersion)
	if err == nil {
		comparison := value
		if requestedRevision == 0 {
			comparison.Revision = existing.Revision
		}
		comparisonHash, hashErr := taskBindingHash(comparison)
		if hashErr != nil {
			return TaskBinding{}, hashErr
		}
		if existingHash != comparisonHash {
			return TaskBinding{}, fmt.Errorf("task binding request key %q was already used with different content", value.RequestKey)
		}
		existing.ProjectID = value.ProjectID
		existing.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
		return existing, err
	}
	if err != sql.ErrNoRows {
		return TaskBinding{}, err
	}
	value.Authority, value.RecordedAt, value.SchemaVersion = "caller_asserted", service.now(), SchemaVersion
	_, err = service.DB.ExecContext(ctx, `INSERT INTO roadmap_task_bindings(project_id,roadmap_node_id,task_id,revision,roadmap_node_revision,status,reason,request_key,request_sha256,authority,recorded_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		value.ProjectID, value.RoadmapNodeID, value.TaskID, value.Revision, value.RoadmapNodeRevision, value.Status, value.Reason, value.RequestKey, requestHash, value.Authority, value.RecordedAt.Format(time.RFC3339Nano), value.SchemaVersion)
	return value, err
}

func taskBindingHash(value TaskBinding) (string, error) {
	value.Authority, value.RecordedAt, value.SchemaVersion = "", time.Time{}, 0
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

type inventoryTaskState struct {
	ID        string
	Managed   bool
	Lifecycle string
	Latest    *TrackCheckpoint
	Count     int
	Roots     int
	Tips      int
}

// TrackInventory returns a deterministic, bounded join. It is deliberately a
// read model: it neither changes checkpoint selection nor repairs bindings.
func (service Service) TrackInventory(ctx context.Context, projectID string, options TrackInventoryOptions) (TrackInventory, error) {
	projectID = strings.TrimSpace(projectID)
	options.Filter = strings.TrimSpace(options.Filter)
	options.RoadmapNodeID = strings.TrimSpace(options.RoadmapNodeID)
	options.TaskID = strings.TrimSpace(options.TaskID)
	if projectID == "" {
		return TrackInventory{}, fmt.Errorf("project ID is required")
	}
	if options.Filter == "" {
		options.Filter = "all"
	}
	if !oneOf(options.Filter, "all", "everything", "current_focus", "active", "resumable", "blocked", "anomalous", "stale") {
		return TrackInventory{}, fmt.Errorf("track inventory filter must be all, everything, current_focus, active, resumable, blocked, anomalous or stale")
	}
	if len(options.TaskID) > 200 {
		return TrackInventory{}, fmt.Errorf("task ID must not exceed 200 bytes")
	}
	if len(options.RoadmapNodeID) > 200 {
		return TrackInventory{}, fmt.Errorf("roadmap node ID must not exceed 200 bytes")
	}
	if options.Limit == 0 {
		options.Limit = DefaultTrackInventoryLimit
	}
	if options.Limit < 1 || options.Limit > MaxTrackInventoryLimit || options.Offset < 0 {
		return TrackInventory{}, fmt.Errorf("limit must be between 1 and %d and offset cannot be negative", MaxTrackInventoryLimit)
	}
	if options.RefLimit == 0 {
		options.RefLimit = DefaultTrackInventoryRefLimit
	}
	if options.RefLimit < 1 || options.RefLimit > MaxTrackInventoryRefLimit {
		return TrackInventory{}, fmt.Errorf("reference limit must be between 1 and %d", MaxTrackInventoryRefLimit)
	}

	nodes, err := service.currentNodes(ctx, projectID)
	if err != nil {
		return TrackInventory{}, err
	}
	links, linksTruncated, err := service.currentNodeLinks(ctx, projectID, maxTrackInventoryLinks)
	if err != nil {
		return TrackInventory{}, err
	}
	if linksTruncated {
		return TrackInventory{}, fmt.Errorf("current roadmap relationships exceed %d-item inventory limit", maxTrackInventoryLinks)
	}
	tracks, _ := roadmapTracks(nodes, links, len(nodes))
	trackByNode := map[string]Track{}
	for _, track := range tracks {
		trackByNode[track.Node.ID] = track
	}
	tasks, err := service.inventoryTasks(ctx, projectID)
	if err != nil {
		return TrackInventory{}, err
	}
	bindings, err := service.currentTaskBindings(ctx, projectID)
	if err != nil {
		return TrackInventory{}, err
	}

	plans, err := service.latestPlanStates(ctx, projectID)
	if err != nil {
		return TrackInventory{}, err
	}
	items := make([]TrackInventoryItem, 0, len(nodes)+len(tasks))
	claimedTasks := map[string]bool{}
	for _, node := range nodes {
		item := TrackInventoryItem{Node: ptrNodeRef(nodeRef(node)), NodeRevision: node.Revision, Tasks: []TrackTaskRef{}, Actions: []TrackActionRef{}, WorkItems: []TrackWorkItemRef{}, Relationships: trackByNode[node.ID], ResumableReasons: []string{}, Anomalies: []string{}}
		activeBindings := bindings[node.ID]
		for _, binding := range activeBindings {
			state, ok := tasks[binding.TaskID]
			if !ok {
				item.Anomalies = appendUniqueString(item.Anomalies, "missing_checkpoint")
				state = inventoryTaskState{ID: binding.TaskID, Lifecycle: "unknown"}
			}
			item.Tasks = append(item.Tasks, TrackTaskRef{TaskID: binding.TaskID, BindingSource: "explicit", BindingRevision: binding.Revision, BindingRoadmapNodeRevision: binding.RoadmapNodeRevision, LifecycleStatus: state.Lifecycle, Managed: state.Managed})
			if binding.RoadmapNodeRevision != node.Revision {
				item.Anomalies = appendUniqueString(item.Anomalies, "stale_task_binding")
			}
			claimedTasks[binding.TaskID] = true
		}
		if state, ok := tasks[node.ID]; ok {
			found := false
			for _, candidate := range item.Tasks {
				found = found || candidate.TaskID == node.ID
			}
			if !found {
				item.Tasks = append(item.Tasks, TrackTaskRef{TaskID: node.ID, BindingSource: "same_id", LifecycleStatus: state.Lifecycle, Managed: state.Managed})
			}
			claimedTasks[node.ID] = true
		}
		sort.Slice(item.Tasks, func(i, j int) bool { return item.Tasks[i].TaskID < item.Tasks[j].TaskID })
		switch {
		case len(activeBindings) == 1:
			// A reviewed explicit binding selects this task even when a legacy
			// same-ID task exists. Keep that task in Tasks for exact audit reads.
			item.BindingState, item.SelectedTaskID = "explicit", activeBindings[0].TaskID
		case len(activeBindings) > 1:
			// A node collects finished tasks over time; only several open ones
			// leave the selection ambiguous.
			if selected, ok := selectNodeTask(item.Tasks, tasks); ok {
				item.BindingState, item.SelectedTaskID = "explicit", selected
			} else {
				item.BindingState = "ambiguous"
				item.Anomalies = append(item.Anomalies, "ambiguous_task_binding")
			}
		case len(item.Tasks) == 0:
			item.BindingState = "missing"
			item.Anomalies = append(item.Anomalies, "missing_task_binding")
		case len(item.Tasks) == 1:
			item.BindingState, item.SelectedTaskID = item.Tasks[0].BindingSource, item.Tasks[0].TaskID
		default:
			item.BindingState = "ambiguous"
			item.Anomalies = append(item.Anomalies, "ambiguous_task_binding")
		}
		if item.SelectedTaskID != "" {
			if err := service.enrichInventoryItem(ctx, projectID, &item, tasks[item.SelectedTaskID], options.RefLimit); err != nil {
				return TrackInventory{}, err
			}
		} else {
			if err := service.enrichWorkItems(ctx, projectID, &item, node.ID, "", options.RefLimit); err != nil {
				return TrackInventory{}, err
			}
		}
		for _, finding := range planFindings(node.Status, len(activeBindings) > 0, plans[node.ID]) {
			item.Anomalies = appendUniqueString(item.Anomalies, finding)
		}
		classifyInventoryItem(&item, node.Status)
		items = append(items, item)
	}
	for taskID, state := range tasks {
		if claimedTasks[taskID] {
			continue
		}
		item := TrackInventoryItem{BindingState: "unbound_task", Tasks: []TrackTaskRef{{TaskID: taskID, BindingSource: "unbound", LifecycleStatus: state.Lifecycle, Managed: state.Managed}}, SelectedTaskID: taskID, Actions: []TrackActionRef{}, WorkItems: []TrackWorkItemRef{}, Relationships: emptyTrack(NodeRef{}), ResumableReasons: []string{}, Anomalies: []string{"task_without_roadmap_node"}}
		if err := service.enrichInventoryItem(ctx, projectID, &item, state, options.RefLimit); err != nil {
			return TrackInventory{}, err
		}
		classifyInventoryItem(&item, "")
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		pi, idi := inventorySortKey(items[i])
		pj, idj := inventorySortKey(items[j])
		return pi < pj || pi == pj && idi < idj
	})
	filtered := items[:0]
	for _, item := range items {
		if options.RoadmapNodeID != "" && (item.Node == nil || item.Node.ID != options.RoadmapNodeID) {
			continue
		}
		if options.TaskID != "" && !inventoryHasTask(item, options.TaskID) {
			continue
		}
		if options.Filter != "everything" && options.TaskID == "" && finishedUnboundTask(item, tasks) {
			continue
		}
		if inventoryMatches(item, options.Filter) {
			filtered = append(filtered, item)
		}
	}
	result := TrackInventory{SchemaVersion: SchemaVersion, ProjectID: projectID, Filter: options.Filter, Items: []TrackInventoryItem{}, Total: len(filtered), Offset: options.Offset, Limit: options.Limit, Authority: "mixed", Disclaimer: "Roadmap, binding, checkpoint, and work item meaning is caller asserted; counts, joins, lifecycle structure, anomaly codes, ordering, and filters are deterministic observations. Resumable states are structural visibility, not selection, scheduling, or authority to execute work."}
	start := options.Offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + options.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	result.Items = append(result.Items, filtered[start:end]...)
	result.Omitted = result.Total - len(result.Items)
	result.Truncated = result.Omitted > 0
	for _, item := range filtered {
		result.Truncated = result.Truncated || item.ActionsTruncated || item.WorkItemsTruncated
	}
	return result, nil
}

func inventoryHasTask(item TrackInventoryItem, taskID string) bool {
	for _, task := range item.Tasks {
		if task.TaskID == taskID {
			return true
		}
	}
	return false
}

// EnsureTaskBinding binds a task to a roadmap node unless an active binding
// already does, and reports whether it appended one. A task with no recorded
// checkpoint or lifecycle event yet is left unbound.
func (service Service) EnsureTaskBinding(ctx context.Context, projectID, nodeID, taskID, reason, requestKey string) (bool, error) {
	current, err := service.currentTaskBindings(ctx, projectID)
	if err != nil {
		return false, err
	}
	for _, binding := range current[nodeID] {
		if binding.TaskID == taskID {
			return false, nil
		}
	}
	var recorded int
	if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT task_id FROM checkpoints WHERE project_id=? AND task_id=?
		UNION SELECT task_id FROM task_lineage_events WHERE project_id=? AND task_id=?)`, projectID, taskID, projectID, taskID).Scan(&recorded); err != nil || recorded == 0 {
		return false, err
	}
	if _, err := service.AppendTaskBinding(ctx, TaskBinding{ProjectID: projectID, RoadmapNodeID: nodeID, TaskID: taskID, Status: "active", Reason: reason, RequestKey: requestKey}); err != nil {
		return false, err
	}
	return true, nil
}

func (service Service) currentTaskBindings(ctx context.Context, projectID string) (map[string][]TaskBinding, error) {
	rows, err := service.DB.QueryContext(ctx, `SELECT b.roadmap_node_id,b.task_id,b.revision,b.roadmap_node_revision,b.status,b.reason,b.request_key,b.authority,b.recorded_at,b.schema_version
		FROM roadmap_task_bindings b WHERE b.project_id=? AND b.revision=(SELECT MAX(latest.revision) FROM roadmap_task_bindings latest WHERE latest.project_id=b.project_id AND latest.roadmap_node_id=b.roadmap_node_id AND latest.task_id=b.task_id)
		ORDER BY b.roadmap_node_id,b.task_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]TaskBinding{}
	for rows.Next() {
		var value TaskBinding
		var recorded string
		if err := rows.Scan(&value.RoadmapNodeID, &value.TaskID, &value.Revision, &value.RoadmapNodeRevision, &value.Status, &value.Reason, &value.RequestKey, &value.Authority, &recorded, &value.SchemaVersion); err != nil {
			return nil, err
		}
		if value.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded); err != nil {
			return nil, err
		}
		value.ProjectID = projectID
		if value.Status == "active" {
			result[value.RoadmapNodeID] = append(result[value.RoadmapNodeID], value)
		}
	}
	return result, rows.Err()
}

func (service Service) inventoryTasks(ctx context.Context, projectID string) (map[string]inventoryTaskState, error) {
	rows, err := service.DB.QueryContext(ctx, `WITH task_ids(task_id) AS (
		SELECT task_id FROM checkpoints WHERE project_id=? AND task_id IS NOT NULL AND task_id<>''
		UNION SELECT task_id FROM task_lineage_events WHERE project_id=?
		UNION SELECT target_task_id FROM task_lineage_events WHERE project_id=? AND target_task_id IS NOT NULL
	) SELECT task_id FROM task_ids ORDER BY task_id LIMIT ?`, projectID, projectID, projectID, maxTrackInventoryTasks+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > maxTrackInventoryTasks {
		return nil, fmt.Errorf("recorded task identities exceed %d-item inventory limit", maxTrackInventoryTasks)
	}
	result := map[string]inventoryTaskState{}
	checkpoints := checkpoint.Service{DB: service.DB}
	for _, id := range ids {
		lineage, err := checkpoints.TaskLineage(ctx, projectID, id, 1)
		if err != nil {
			return nil, err
		}
		state := inventoryTaskState{ID: id, Managed: lineage.State.Managed, Lifecycle: lineage.State.Status}
		var checkpointID, status, nextAction, created string
		err = service.DB.QueryRowContext(ctx, `SELECT id,status,next_action,created_at FROM checkpoints WHERE project_id=? AND task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, projectID, id).Scan(&checkpointID, &status, &nextAction, &created)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			when, parseErr := time.Parse(time.RFC3339Nano, created)
			if parseErr != nil {
				return nil, parseErr
			}
			state.Latest = &TrackCheckpoint{ID: checkpointID, Status: status, NextAction: nextAction, CreatedAt: when}
		}
		if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER (WHERE predecessor_checkpoint_id IS NULL) FROM checkpoints WHERE project_id=? AND task_id=?`, projectID, id).Scan(&state.Count, &state.Roots); err != nil {
			return nil, err
		}
		if err := service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints c WHERE c.project_id=? AND c.task_id=? AND NOT EXISTS(SELECT 1 FROM checkpoints child WHERE child.project_id=c.project_id AND child.predecessor_checkpoint_id=c.id)`, projectID, id).Scan(&state.Tips); err != nil {
			return nil, err
		}
		result[id] = state
	}
	return result, nil
}

func (service Service) enrichInventoryItem(ctx context.Context, projectID string, item *TrackInventoryItem, state inventoryTaskState, refLimit int) error {
	item.LatestCheckpoint = state.Latest
	if state.Latest == nil {
		item.Anomalies = appendUniqueString(item.Anomalies, "missing_checkpoint")
	}
	// Pre-lineage checkpoints may have several historical roots and tips, but
	// they are not resolvable modern forks. Preserve their counts for exact
	// inspection without labeling them as current lineage anomalies.
	if state.Managed && state.Roots > 1 {
		item.Anomalies = appendUniqueString(item.Anomalies, "orphan")
	}
	if state.Managed && state.Tips > 1 {
		item.Anomalies = appendUniqueString(item.Anomalies, "multiple_tip")
	}
	if state.Managed && ((state.Latest != nil && state.Latest.Status == "completed" && state.Lifecycle == "active") || (state.Lifecycle != "active" && state.Latest != nil)) {
		item.Anomalies = appendUniqueString(item.Anomalies, "terminal_selection")
	}
	rows, err := service.DB.QueryContext(ctx, `WITH latest AS (
		SELECT t.action_id,t.status,ROW_NUMBER() OVER(PARTITION BY t.action_id ORDER BY t.created_at DESC,t.id DESC) n
		FROM checkpoint_action_transitions t JOIN checkpoint_actions a ON a.id=t.action_id WHERE a.project_id=?
	) SELECT a.id,a.source_checkpoint_id,l.status FROM checkpoint_actions a JOIN latest l ON l.action_id=a.id AND l.n=1 JOIN checkpoints c ON c.id=a.source_checkpoint_id
	WHERE a.project_id=? AND c.task_id=? AND l.status IN ('open','carried_forward') ORDER BY a.created_at,a.id`, projectID, projectID, state.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ref TrackActionRef
		if err := rows.Scan(&ref.ID, &ref.SourceCheckpointID, &ref.Status); err != nil {
			rows.Close()
			return err
		}
		item.UnresolvedActionCount++
		if len(item.Actions) < refLimit {
			item.Actions = append(item.Actions, ref)
		} else {
			item.ActionsTruncated = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	nodeID := ""
	if item.Node != nil {
		nodeID = item.Node.ID
	}
	return service.enrichWorkItems(ctx, projectID, item, nodeID, state.ID, refLimit)
}

func (service Service) enrichWorkItems(ctx context.Context, projectID string, item *TrackInventoryItem, nodeID, taskID string, refLimit int) error {
	rows, err := service.DB.QueryContext(ctx, `SELECT w.id,w.kind,COALESCE((SELECT d.status FROM work_item_dispositions d WHERE d.project_id=w.project_id AND d.item_id=w.id ORDER BY d.recorded_at DESC,d.id DESC LIMIT 1),'pending') current_status
		FROM work_items w WHERE w.project_id=? AND ((?<>'' AND w.roadmap_node_id=?) OR (?<>'' AND w.task_id=?)) ORDER BY w.recorded_at,w.id`, projectID, nodeID, nodeID, taskID, taskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref TrackWorkItemRef
		if err := rows.Scan(&ref.ID, &ref.Kind, &ref.Status); err != nil {
			return err
		}
		switch ref.Status {
		case "pending":
			item.PendingWorkItems++
		case "accepted":
			item.AcceptedWorkItems++
		}
		if len(item.WorkItems) < refLimit {
			item.WorkItems = append(item.WorkItems, ref)
		} else {
			item.WorkItemsTruncated = true
		}
	}
	return rows.Err()
}

func classifyInventoryItem(item *TrackInventoryItem, roadmapStatus string) {
	item.CurrentFocus = roadmapStatus == "accepted" || roadmapStatus == "active"
	item.Active = roadmapStatus == "active"
	item.Blocked = roadmapStatus == "blocked" || len(item.Relationships.BlockedBy) > 0 || len(item.Relationships.UnresolvedDependencies) > 0
	for _, task := range item.Tasks {
		if task.TaskID != item.SelectedTaskID {
			continue
		}
		if task.Managed && task.LifecycleStatus == "active" && item.LatestCheckpoint != nil && item.LatestCheckpoint.Status != "completed" {
			item.Active = true
			item.ResumableReasons = append(item.ResumableReasons, "nonterminal_managed_tip")
		}
	}
	if (roadmapStatus == "deferred" || roadmapStatus == "proposed") && item.LatestCheckpoint != nil && openCheckpointStatus(item.LatestCheckpoint.Status) {
		item.Anomalies = appendUniqueString(item.Anomalies, "work_on_inactive_node")
	}
	if roadmapStatus == "accepted" && item.SelectedTaskID == "" {
		item.ResumableReasons = append(item.ResumableReasons, "accepted_roadmap_work_without_task")
	}
	item.Resumable = len(item.ResumableReasons) > 0 && !item.Blocked
	if item.Node != nil && item.LatestCheckpoint != nil {
		// A node not yet started may have a finished precursor task.
		started := item.Node.Status == "active" || item.Node.Status == "blocked"
		// An abandoned or superseded task is finished whatever its last checkpoint says.
		ended := false
		for _, task := range item.Tasks {
			if task.TaskID == item.SelectedTaskID {
				ended = task.LifecycleStatus == "abandoned" || task.LifecycleStatus == "superseded" || task.LifecycleStatus == "rekeyed"
			}
		}
		if (item.Node.Status == "completed" && item.LatestCheckpoint.Status != "completed" && !ended) || (started && item.LatestCheckpoint.Status == "completed") {
			item.Anomalies = appendUniqueString(item.Anomalies, "roadmap_checkpoint_status")
		}
	}
	sort.Strings(item.Anomalies)
}

// selectNodeTask picks among a node's bound tasks: the one open task, or when
// all are finished the most recently checkpointed one, preferring tasks that
// finished over abandoned or superseded ones. Several open tasks cannot be
// told apart.
func selectNodeTask(refs []TrackTaskRef, tasks map[string]inventoryTaskState) (string, bool) {
	var open []string
	latest, latestRank, latestAt := "", 0, time.Time{}
	for _, ref := range refs {
		state := tasks[ref.TaskID]
		if state.Latest == nil {
			continue
		}
		if taskOpen(state) {
			open = append(open, ref.TaskID)
		}
		rank := 1
		if state.Lifecycle == "abandoned" || state.Lifecycle == "superseded" || state.Lifecycle == "rekeyed" {
			rank = 0
		}
		if latest == "" || rank > latestRank || rank == latestRank && state.Latest.CreatedAt.After(latestAt) {
			latest, latestRank, latestAt = ref.TaskID, rank, state.Latest.CreatedAt
		}
	}
	switch len(open) {
	case 0:
		return latest, latest != ""
	case 1:
		return open[0], true
	}
	return "", false
}

// taskOpen reports whether a task can still take work: its lifecycle is not
// terminal and its latest checkpoint is not completed.
func taskOpen(state inventoryTaskState) bool {
	switch state.Lifecycle {
	case "abandoned", "superseded", "rekeyed", "completed":
		return false
	}
	return state.Latest != nil && openCheckpointStatus(state.Latest.Status)
}

func openCheckpointStatus(status string) bool {
	return status != "completed" && status != "abandoned" && status != "superseded"
}

// finishedUnboundTask reports a task with no roadmap node that can take no
// more work; the all filter hides these, everything shows them.
func finishedUnboundTask(item TrackInventoryItem, tasks map[string]inventoryTaskState) bool {
	return item.Node == nil && item.BindingState == "unbound_task" && !taskOpen(tasks[item.SelectedTaskID])
}

func inventoryMatches(item TrackInventoryItem, filter string) bool {
	switch filter {
	case "current_focus":
		return item.CurrentFocus
	case "active":
		return item.Active
	case "resumable":
		return item.Resumable
	case "blocked":
		return item.Blocked
	case "anomalous":
		return len(item.Anomalies) > 0
	case "stale":
		actionableAnomaly := len(item.Anomalies) > 0 && (item.CurrentFocus || item.Active || item.Resumable)
		terminalOpenAction := item.LatestCheckpoint != nil && item.LatestCheckpoint.Status == "completed" && item.UnresolvedActionCount > 0
		return actionableAnomaly || terminalOpenAction
	default:
		return true
	}
}

func inventorySortKey(item TrackInventoryItem) (int, string) {
	if item.Node != nil {
		return item.Node.Priority, item.Node.ID
	}
	return 101, item.SelectedTaskID
}

func ptrNodeRef(value NodeRef) *NodeRef { return &value }

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type planState struct {
	status    string
	openItems int
}

// latestPlanStates returns the latest revision status and open-item count of each
// track plan, grouped by roadmap node.
func (service Service) latestPlanStates(ctx context.Context, projectID string) (map[string][]planState, error) {
	rows, err := service.DB.QueryContext(ctx, `SELECT p.roadmap_node_id, p.status,
		(SELECT COUNT(*) FROM track_plan_items i WHERE i.project_id=p.project_id AND i.plan_id=p.id AND i.plan_revision=p.revision AND i.status NOT IN ('completed','superseded'))
		FROM track_plan_revisions p
		JOIN (SELECT id, COALESCE(MAX(CASE WHEN status!='proposed' THEN revision END),MAX(revision)) revision FROM track_plan_revisions WHERE project_id=? GROUP BY id) latest ON latest.id=p.id AND latest.revision=p.revision
		WHERE p.project_id=?`, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string][]planState{}
	for rows.Next() {
		var node string
		var state planState
		if err = rows.Scan(&node, &state.status, &state.openItems); err != nil {
			return nil, err
		}
		states[node] = append(states[node], state)
	}
	return states, rows.Err()
}

// planFindings reports track-plan drift for one roadmap node. Nodes with an
// explicit task binding are expected to have a reviewed plan; older nodes without
// bindings predate track plans and are not flagged as missing one.
func planFindings(nodeStatus string, bound bool, plans []planState) []string {
	var live []planState
	for _, plan := range plans {
		if plan.status != "superseded" {
			live = append(live, plan)
		}
	}
	var findings []string
	// A finished or set-aside node without a plan is history, not a gap.
	open := nodeStatus == "proposed" || nodeStatus == "accepted" || nodeStatus == "active" || nodeStatus == "blocked"
	if bound && len(live) == 0 && open {
		findings = append(findings, "missing_track_plan")
	}
	if len(live) == 0 {
		return findings
	}
	reviewed, finished := false, true
	for _, plan := range live {
		reviewed = reviewed || plan.status != "proposed"
		finished = finished && plan.status == "completed" && plan.openItems == 0
	}
	if bound && !reviewed {
		findings = append(findings, "track_plan_unreviewed")
	}
	if nodeStatus == "deferred" || nodeStatus == "proposed" {
		for _, plan := range live {
			if plan.status == "accepted" || plan.status == "active" {
				findings = append(findings, "work_on_inactive_node")
				break
			}
		}
	}
	switch nodeStatus {
	case "completed":
		if !finished {
			findings = append(findings, "track_plan_behind")
		}
	case "proposed", "accepted", "active", "blocked":
		if finished {
			findings = append(findings, "track_plan_behind")
		}
	}
	return findings
}

// NodePlanFindings reports the track-plan findings for one roadmap node at the
// given status, using the same rules as the inventory.
func (service Service) NodePlanFindings(ctx context.Context, projectID, nodeID, status string) ([]string, error) {
	plans, err := service.latestPlanStates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var bindings int
	if err = service.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_task_bindings b WHERE b.project_id=? AND b.roadmap_node_id=? AND b.status='active' AND b.revision=(SELECT MAX(x.revision) FROM roadmap_task_bindings x WHERE x.project_id=b.project_id AND x.roadmap_node_id=b.roadmap_node_id AND x.task_id=b.task_id)`, projectID, nodeID).Scan(&bindings); err != nil {
		return nil, err
	}
	return planFindings(status, bindings > 0, plans[nodeID]), nil
}
