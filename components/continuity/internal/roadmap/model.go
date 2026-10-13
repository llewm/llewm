package roadmap

import "time"

const SchemaVersion = 1

const DefaultProjectionID = "roadmap"

type Node struct {
	ProjectID     string    `json:"project_id"`
	ID            string    `json:"id"`
	Revision      int       `json:"revision"`
	Title         string    `json:"title"`
	Status        string    `json:"status"`
	Priority      int       `json:"priority"`
	Kind          string    `json:"kind"`
	TargetDate    string    `json:"target_date,omitempty"`
	TargetRelease string    `json:"target_release,omitempty"`
	TargetHorizon string    `json:"target_horizon,omitempty"`
	EvidenceRefs  []string  `json:"evidence_refs"`
	SourcePath    string    `json:"source_path"`
	SourceRef     string    `json:"source_ref,omitempty"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

type Binding struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	NodeID        string    `json:"node_id"`
	NodeRevision  int       `json:"node_revision"`
	Path          string    `json:"path"`
	PathKind      string    `json:"path_kind"`
	Relationship  string    `json:"relationship"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

// TaskBinding is one immutable revision of an explicit roadmap-node/task
// association. The current assertion for a node/task pair is its highest
// revision; several active pairs for one node remain visible as an ambiguity.
type TaskBinding struct {
	ProjectID           string    `json:"project_id"`
	RoadmapNodeID       string    `json:"roadmap_node_id"`
	TaskID              string    `json:"task_id"`
	Revision            int       `json:"revision"`
	RoadmapNodeRevision int       `json:"roadmap_node_revision"`
	Status              string    `json:"status"`
	Reason              string    `json:"reason"`
	RequestKey          string    `json:"request_key"`
	Authority           string    `json:"authority"`
	RecordedAt          time.Time `json:"recorded_at"`
	SchemaVersion       int       `json:"schema_version"`
}

type TrackTaskRef struct {
	TaskID                     string `json:"task_id"`
	BindingSource              string `json:"binding_source"`
	BindingRevision            int    `json:"binding_revision,omitempty"`
	BindingRoadmapNodeRevision int    `json:"binding_roadmap_node_revision,omitempty"`
	LifecycleStatus            string `json:"lifecycle_status"`
	Managed                    bool   `json:"managed"`
}

type TrackCheckpoint struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	NextAction string    `json:"next_action"`
	CreatedAt  time.Time `json:"created_at"`
}

type TrackActionRef struct {
	ID                 string `json:"id"`
	SourceCheckpointID string `json:"source_checkpoint_id"`
	Status             string `json:"status"`
}

type TrackWorkItemRef struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type TrackInventoryItem struct {
	Node                  *NodeRef           `json:"node,omitempty"`
	NodeRevision          int                `json:"node_revision,omitempty"`
	BindingState          string             `json:"binding_state"`
	Tasks                 []TrackTaskRef     `json:"tasks"`
	SelectedTaskID        string             `json:"selected_task_id,omitempty"`
	LatestCheckpoint      *TrackCheckpoint   `json:"latest_checkpoint,omitempty"`
	UnresolvedActionCount int                `json:"unresolved_action_count"`
	Actions               []TrackActionRef   `json:"actions"`
	ActionsTruncated      bool               `json:"actions_truncated"`
	PendingWorkItems      int                `json:"pending_work_item_count"`
	AcceptedWorkItems     int                `json:"accepted_work_item_count"`
	WorkItems             []TrackWorkItemRef `json:"work_items"`
	WorkItemsTruncated    bool               `json:"work_items_truncated"`
	Relationships         Track              `json:"relationships"`
	CurrentFocus          bool               `json:"current_focus"`
	Active                bool               `json:"active"`
	Blocked               bool               `json:"blocked"`
	Resumable             bool               `json:"resumable"`
	ResumableReasons      []string           `json:"resumable_reasons"`
	Anomalies             []string           `json:"anomalies"`
}

type TrackInventoryOptions struct {
	Filter        string `json:"filter,omitempty"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	RefLimit      int    `json:"ref_limit,omitempty"`
}

type TrackInventory struct {
	SchemaVersion int                  `json:"schema_version"`
	ProjectID     string               `json:"project_id"`
	Filter        string               `json:"filter"`
	Items         []TrackInventoryItem `json:"items"`
	Total         int                  `json:"total"`
	Offset        int                  `json:"offset"`
	Limit         int                  `json:"limit"`
	Omitted       int                  `json:"omitted"`
	Truncated     bool                 `json:"truncated"`
	Authority     string               `json:"authority"`
	Disclaimer    string               `json:"disclaimer"`
}

// TrackReconciliationPlanOptions selects either a bounded discovery view or one
// exact caller-reviewed operation. Empty operation means inspect only.
type TrackReconciliationPlanOptions struct {
	Operation     string `json:"operation,omitempty"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	TargetTaskID  string `json:"target_task_id,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// TrackReconciliationOperation is an exact, inert proposal suitable for review
// before invoking the existing task-transition or task-binding write surface.
type TrackReconciliationOperation struct {
	Kind                string `json:"kind"`
	Operation           string `json:"operation"`
	TaskID              string `json:"task_id"`
	TargetTaskID        string `json:"target_task_id,omitempty"`
	CheckpointID        string `json:"checkpoint_id,omitempty"`
	RoadmapNodeID       string `json:"roadmap_node_id,omitempty"`
	RoadmapNodeRevision int    `json:"roadmap_node_revision,omitempty"`
	BindingRevision     int    `json:"binding_revision,omitempty"`
	BindingStatus       string `json:"binding_status,omitempty"`
	Reason              string `json:"reason"`
	RequestKey          string `json:"request_key"`
	Authority           string `json:"authority"`
}

type TrackReconciliationItem struct {
	Node                 *NodeRef                       `json:"node,omitempty"`
	NodeRevision         int                            `json:"node_revision,omitempty"`
	TaskID               string                         `json:"task_id,omitempty"`
	TargetTaskID         string                         `json:"target_task_id,omitempty"`
	LifecycleStatus      string                         `json:"lifecycle_status,omitempty"`
	Managed              bool                           `json:"managed"`
	CheckpointCount      int                            `json:"checkpoint_count"`
	RootCount            int                            `json:"root_count"`
	TipCount             int                            `json:"tip_count"`
	SelectedCheckpointID string                         `json:"selected_checkpoint_id,omitempty"`
	Proposals            []TrackReconciliationOperation `json:"proposals"`
	Blockers             []string                       `json:"blockers"`
}

type TrackReconciliationPlan struct {
	SchemaVersion int                       `json:"schema_version"`
	ProjectID     string                    `json:"project_id"`
	Operation     string                    `json:"operation"`
	Items         []TrackReconciliationItem `json:"items"`
	Total         int                       `json:"total"`
	Limit         int                       `json:"limit"`
	Truncated     bool                      `json:"truncated"`
	ReadOnly      bool                      `json:"read_only"`
	Authority     string                    `json:"authority"`
	Disclaimer    string                    `json:"disclaimer"`
}

type NodeLink struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	NodeID        string    `json:"node_id"`
	NodeRevision  int       `json:"node_revision"`
	TargetNodeID  string    `json:"target_node_id"`
	Relationship  string    `json:"relationship"`
	Authority     string    `json:"authority"`
	RecordedAt    time.Time `json:"recorded_at"`
	SchemaVersion int       `json:"schema_version"`
}

type SyncRun struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	GitCommit     string    `json:"git_commit"`
	Client        string    `json:"client,omitempty"`
	Model         string    `json:"model,omitempty"`
	InvocationID  string    `json:"invocation_id,omitempty"`
	ReviewedNodes []string  `json:"reviewed_nodes"`
	ReviewedFiles []string  `json:"reviewed_files"`
	Authority     string    `json:"authority"`
	GitAuthority  string    `json:"git_authority"`
	CompletedAt   time.Time `json:"completed_at"`
	SchemaVersion int       `json:"schema_version"`
}

type ReviewRoutine struct {
	ProjectID               string    `json:"project_id"`
	ID                      string    `json:"id"`
	Revision                int       `json:"revision"`
	Title                   string    `json:"title"`
	Status                  string    `json:"status"`
	CommitThreshold         *int      `json:"commit_threshold,omitempty"`
	ElapsedSecondsThreshold *int64    `json:"elapsed_seconds_threshold,omitempty"`
	ChangedFilesThreshold   *int      `json:"changed_files_threshold,omitempty"`
	Source                  string    `json:"source"`
	SourceRef               string    `json:"source_ref,omitempty"`
	Authority               string    `json:"authority"`
	RecordedAt              time.Time `json:"recorded_at"`
	SchemaVersion           int       `json:"schema_version"`
}

type FileChange struct {
	Path          string   `json:"path"`
	State         string   `json:"state"`
	PreviousHash  string   `json:"previous_hash,omitempty"`
	CurrentHash   string   `json:"current_hash,omitempty"`
	NodeIDs       []string `json:"node_ids"`
	Relationships []string `json:"relationships"`
}

type AffectedNode struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Revision     int      `json:"revision"`
	ChangedFiles []string `json:"changed_files"`
}

type Status struct {
	SchemaVersion    int            `json:"schema_version"`
	ProjectID        string         `json:"project_id"`
	CurrentCommit    string         `json:"current_commit,omitempty"`
	WorkingTreeDirty bool           `json:"working_tree_dirty"`
	LastSync         *SyncRun       `json:"last_sync,omitempty"`
	CommitsSinceSync *int           `json:"commits_since_sync,omitempty"`
	ElapsedSeconds   *int64         `json:"elapsed_seconds,omitempty"`
	ChangedFiles     []FileChange   `json:"changed_files"`
	AffectedNodes    []AffectedNode `json:"affected_nodes"`
	Freshness        string         `json:"freshness"`
	ReviewRoutine    *ReviewRoutine `json:"review_routine,omitempty"`
	ReviewDue        bool           `json:"review_due"`
	DueReasons       []string       `json:"due_reasons"`
	Disclaimer       string         `json:"disclaimer"`
}

type ReviewSource struct {
	Path      string `json:"path"`
	SourceRef string `json:"source_ref,omitempty"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated"`
	Authority string `json:"authority"`
}

type ReviewPacket struct {
	SchemaVersion int            `json:"schema_version"`
	ProjectID     string         `json:"project_id"`
	Status        Status         `json:"status"`
	Nodes         []Node         `json:"nodes"`
	Sources       []ReviewSource `json:"sources"`
	ByteBudget    int            `json:"byte_budget"`
	ContentBytes  int            `json:"content_bytes"`
	Truncated     bool           `json:"truncated"`
	Trigger       string         `json:"trigger"`
	Disclaimer    string         `json:"disclaimer"`
}

type NodeRef struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Kind     string `json:"kind"`
}

type Track struct {
	Node                   NodeRef   `json:"node"`
	Parents                []NodeRef `json:"parents"`
	Subtasks               []NodeRef `json:"subtasks"`
	DependsOn              []NodeRef `json:"depends_on"`
	UnresolvedDependencies []NodeRef `json:"unresolved_dependencies"`
	BlockedBy              []NodeRef `json:"blocked_by"`
	Blocks                 []NodeRef `json:"blocks"`
	ParallelWith           []NodeRef `json:"parallel_with"`
	Explores               []NodeRef `json:"explores"`
}

type ReviewTrigger struct {
	Kind            string `json:"kind"`
	Threshold       *int64 `json:"threshold,omitempty"`
	Observed        *int64 `json:"observed,omitempty"`
	State           string `json:"state"`
	RoutineID       string `json:"routine_id"`
	RoutineRevision int    `json:"routine_revision"`
}

type Overview struct {
	SchemaVersion         int             `json:"schema_version"`
	ProjectID             string          `json:"project_id"`
	Status                Status          `json:"status"`
	CurrentFocus          []Node          `json:"current_focus"`
	Blocked               []Node          `json:"blocked"`
	Future                []Node          `json:"future"`
	Completed             []Node          `json:"completed"`
	ChangedNodesSinceSync []Node          `json:"changed_nodes_since_sync"`
	Relationships         []NodeLink      `json:"relationships"`
	Tracks                []Track         `json:"tracks"`
	ReviewTriggers        []ReviewTrigger `json:"review_triggers"`
	Limit                 int             `json:"limit"`
	Truncated             bool            `json:"truncated"`
	Disclaimer            string          `json:"disclaimer"`
}

type SnapshotNode struct {
	ID            string   `json:"id"`
	Revision      int      `json:"revision"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	Priority      int      `json:"priority"`
	Kind          string   `json:"kind"`
	TargetDate    string   `json:"target_date,omitempty"`
	TargetRelease string   `json:"target_release,omitempty"`
	TargetHorizon string   `json:"target_horizon,omitempty"`
	EvidenceRefs  []string `json:"evidence_refs"`
	SourcePath    string   `json:"source_path"`
	SourceRef     string   `json:"source_ref,omitempty"`
}

type SnapshotBinding struct {
	NodeID       string `json:"node_id"`
	NodeRevision int    `json:"node_revision"`
	Path         string `json:"path"`
	PathKind     string `json:"path_kind"`
	Relationship string `json:"relationship"`
}

type SnapshotRelationship struct {
	NodeID       string `json:"node_id"`
	NodeRevision int    `json:"node_revision"`
	Relationship string `json:"relationship"`
	TargetNodeID string `json:"target_node_id"`
}

type SnapshotTrigger struct {
	Kind            string `json:"kind"`
	Threshold       *int64 `json:"threshold,omitempty"`
	Observation     string `json:"observation"`
	State           string `json:"state"`
	RoutineID       string `json:"routine_id"`
	RoutineRevision int    `json:"routine_revision"`
}

// GraphSnapshot is the canonical, read-only input to roadmap projection. It
// intentionally excludes append record IDs, timestamps, and local project paths.
type GraphSnapshot struct {
	SchemaVersion  int                    `json:"schema_version"`
	ProjectID      string                 `json:"project_id"`
	ProjectName    string                 `json:"project_name"`
	GraphHash      string                 `json:"graph_hash"`
	Nodes          []SnapshotNode         `json:"nodes"`
	Bindings       []SnapshotBinding      `json:"bindings"`
	Relationships  []SnapshotRelationship `json:"relationships"`
	Tracks         []Track                `json:"tracks"`
	ReviewTriggers []SnapshotTrigger      `json:"review_triggers"`
}

type Projection struct {
	SchemaVersion   int    `json:"schema_version"`
	ProjectID       string `json:"project_id"`
	GraphHash       string `json:"graph_hash"`
	RendererName    string `json:"renderer_name"`
	RendererVersion int    `json:"renderer_version"`
	RendererHash    string `json:"renderer_hash"`
	ContentHash     string `json:"content_hash"`
	Markdown        string `json:"markdown"`
}

type ProjectionCheck struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Path          string `json:"path"`
	State         string `json:"state"`
	ExpectedHash  string `json:"expected_hash"`
	ActualHash    string `json:"actual_hash,omitempty"`
	GraphHash     string `json:"graph_hash"`
	RendererHash  string `json:"renderer_hash"`
	Disclaimer    string `json:"disclaimer"`
}

type AuthoritySource struct {
	Path          string `json:"path"`
	SourceKind    string `json:"source_kind"`
	ContentSHA256 string `json:"content_sha256"`
}

type AuthorityDestination struct {
	Path             string `json:"path"`
	PrewriteState    string `json:"prewrite_state"`
	PrewriteSHA256   string `json:"prewrite_sha256,omitempty"`
	ProjectionSHA256 string `json:"projection_sha256"`
}

type AuthorityPlan struct {
	SchemaVersion   int                    `json:"schema_version"`
	ProjectID       string                 `json:"project_id"`
	ProjectionID    string                 `json:"projection_id"`
	PreviousMode    string                 `json:"previous_mode"`
	TargetMode      string                 `json:"target_mode"`
	PlanSHA256      string                 `json:"plan_sha256"`
	GraphSHA256     string                 `json:"graph_sha256"`
	RendererName    string                 `json:"renderer_name"`
	RendererVersion int                    `json:"renderer_version"`
	RendererSHA256  string                 `json:"renderer_sha256"`
	Sources         []AuthoritySource      `json:"sources"`
	Destinations    []AuthorityDestination `json:"destinations"`
	Candidate       Projection             `json:"candidate"`
	Disclaimer      string                 `json:"disclaimer"`
}

type GenerationResult struct {
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	ProjectionID  string `json:"projection_id"`
	PlanSHA256    string `json:"plan_sha256"`
	Path          string `json:"path"`
	State         string `json:"state"`
	SHA256        string `json:"sha256"`
	Disclaimer    string `json:"disclaimer"`
}

type AuthorityMigration struct {
	ID                  string                 `json:"id"`
	ProjectID           string                 `json:"project_id"`
	ProjectionID        string                 `json:"projection_id"`
	PreviousMode        string                 `json:"previous_mode"`
	TargetMode          string                 `json:"target_mode"`
	PlanSHA256          string                 `json:"plan_sha256"`
	GraphSHA256         string                 `json:"graph_sha256"`
	RendererName        string                 `json:"renderer_name"`
	RendererVersion     int                    `json:"renderer_version"`
	RendererSHA256      string                 `json:"renderer_sha256"`
	Sources             []AuthoritySource      `json:"sources"`
	Destinations        []AuthorityDestination `json:"destinations"`
	Creator             string                 `json:"creator"`
	CreatorInvocationID string                 `json:"creator_invocation_id,omitempty"`
	ReviewAuthority     string                 `json:"review_authority"`
	FileAuthority       string                 `json:"file_authority"`
	RecordedAt          time.Time              `json:"recorded_at"`
	SchemaVersion       int                    `json:"schema_version"`
}
