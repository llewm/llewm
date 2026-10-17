package mcpserver

import (
	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/config"

	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/llewm/llewm/components/continuity/internal/brief"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/friction"
	"github.com/llewm/llewm/components/continuity/internal/history"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/pin"
	"github.com/llewm/llewm/components/continuity/internal/presence"
	"github.com/llewm/llewm/components/continuity/internal/procedures"
	"github.com/llewm/llewm/components/continuity/internal/projectpulse"
	"github.com/llewm/llewm/components/continuity/internal/readiness"
	"github.com/llewm/llewm/components/continuity/internal/release"
	"github.com/llewm/llewm/components/continuity/internal/researchcapture"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/roadmap"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
	"github.com/llewm/llewm/components/continuity/internal/usage"
	"github.com/llewm/llewm/components/continuity/internal/workitem"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
	"github.com/llewm/llewm/components/continuity/internal/workstate"
)

type EmptyInput struct{}

func optionalRFC3339(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

// BootstrapInput selects a checkpoint, task, or session, plus the caller's own
// current session, which lets an empty selection prefer its live task presence.
type BootstrapInput struct {
	CheckpointID     string `json:"checkpoint_id,omitempty" jsonschema:"select this checkpoint"`
	TaskID           string `json:"task_id,omitempty" jsonschema:"select this task"`
	SessionID        string `json:"session_id,omitempty" jsonschema:"select this session's checkpoint"`
	CurrentSessionID string `json:"current_session_id,omitempty" jsonschema:"your current client session ID; prefers its live task presence"`
	Full             bool   `json:"full,omitempty" jsonschema:"return the packet even if unchanged since SessionStart"`
}

type BootstrapOutput struct {
	Markdown       string `json:"markdown" jsonschema:"compact continuity bootstrap for the active project, or a one-line notice when unchanged"`
	Unchanged      bool   `json:"unchanged,omitempty" jsonschema:"the session's SessionStart bootstrap already holds this exact packet"`
	RuntimeWarning string `json:"runtime_warning,omitempty" jsonschema:"bounded warning when the running MCP process needs restart"`
}

type CheckpointInput struct {
	SessionID               string                    `json:"session_id,omitempty" jsonschema:"bootstrap's This session ID"`
	TaskID                  string                    `json:"task_id" jsonschema:"new ID starts a task"`
	PredecessorCheckpointID string                    `json:"predecessor_checkpoint_id,omitempty" jsonschema:"task's current tip; required after its first checkpoint"`
	ReviewsTaskID           string                    `json:"reviews_task_id,omitempty" jsonschema:"task this work reviews or repairs"`
	RequestKey              string                    `json:"request_key,omitempty" jsonschema:"idempotent retry key"`
	Task                    string                    `json:"task"`
	Status                  string                    `json:"status,omitempty"`
	Summary                 string                    `json:"summary"`
	NextAction              string                    `json:"next_action"`
	Completed               []string                  `json:"completed,omitempty"`
	Changes                 []string                  `json:"changes,omitempty"`
	Verified                []string                  `json:"verified,omitempty"`
	Attempts                []checkpoint.Attempt      `json:"attempts,omitempty"`
	EvidenceRefs            []string                  `json:"evidence_refs,omitempty" jsonschema:"refs such as commit:HASH or decision:ID"`
	ActionUpdates           []checkpoint.ActionUpdate `json:"action_updates,omitempty" jsonschema:"complete or supersede open predecessor actions"`
	CarryForwardActionIDs   []string                  `json:"carry_forward_action_ids,omitempty" jsonschema:"open predecessor actions to carry forward"`
	DryRun                  bool                      `json:"dry_run,omitempty" jsonschema:"validate without saving"`
	LeaseHolder             string                    `json:"lease_holder,omitempty" jsonschema:"holder of your lease on this scope"`
	LeaseSession            string                    `json:"lease_session,omitempty"`
	LeaseToken              int64                     `json:"lease_token,omitempty"`
}

type CheckpointOutput struct {
	ID             string              `json:"id" jsonschema:"created checkpoint identifier"`
	Deferred       bool                `json:"deferred,omitempty" jsonschema:"checkpoint was not saved; a device-local recovery envelope was staged for a current writer"`
	RecoveryID     string              `json:"recovery_id,omitempty" jsonschema:"device-local recovery envelope to apply after restart"`
	RecoveryNotice string              `json:"recovery_notice,omitempty" jsonschema:"required recovery procedure when deferred is true"`
	CarriedForward []checkpoint.Action `json:"carried_forward" jsonschema:"unresolved predecessor actions carried into this checkpoint"`
	Reconciled     []checkpoint.Action `json:"reconciled" jsonschema:"predecessor actions atomically completed or superseded by this checkpoint"`
	ActionCreated  bool                `json:"action_created" jsonschema:"whether this nonterminal checkpoint created an open next action"`
	SessionLease   string              `json:"session_lease,omitempty"`
	Lease          *lease.Stamp        `json:"lease,omitempty" jsonschema:"which lease the checkpoint was written under: leased, unleased, or lapsed (you presented a token that no longer holds); set when the checkpoint has a task_id"`
	// UnnarratedCommits is machine-observed narrative coverage; it never blocks the checkpoint.
	UnnarratedCommits []worknarrative.UnnarratedCommit `json:"unnarrated_commits,omitempty" jsonschema:"the engineer's recent commits (last 14 days) that no narrative describes; record one per commit with continuity_narrative_record and commit"`
	// ResearchCapture compares bounded discovery activity with task-linked durable output.
	ResearchCapture *researchcapture.Report `json:"research_capture,omitempty" jsonschema:"advisory comparison of substantial discovery activity with task-linked durable records and checkpoint evidence"`
	Preflight       *bootstrap.Readiness    `json:"preflight,omitempty" jsonschema:"dry run result; nothing was saved"`
}

// CheckpointGetInput exports one checkpoint by ID or finds checkpoints by text.
type CheckpointGetInput struct {
	ID       string `json:"id,omitempty" jsonschema:"checkpoint to export"`
	MaxBytes int    `json:"max_bytes,omitempty"`
	Query    string `json:"query,omitempty" jsonschema:"literal text to find"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

// CheckpointGetResult holds the exported checkpoint or the search matches.
type CheckpointGetResult struct {
	Checkpoint *checkpoint.Export     `json:"checkpoint,omitempty"`
	Matches    *checkpoint.FindResult `json:"matches,omitempty"`
}

type CheckpointActionInput struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"your task; refuses others' actions"`
	ID     string `json:"id"`
	Status string `json:"status" jsonschema:"completed or superseded"`
	Reason string `json:"reason,omitempty"`
}

type CheckpointActionsInput struct {
	TaskID string `json:"task_id,omitempty" jsonschema:"list this task's actions"`
	All    bool   `json:"all,omitempty" jsonschema:"every task's actions"`
}

type CheckpointActionsOutput struct {
	Actions []checkpoint.Action `json:"actions"`
}

type ProcedureShowInput struct {
	ID       string `json:"id,omitempty" jsonschema:"omit to list"`
	Revision int    `json:"revision,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Status   string `json:"status,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// ProcedureResult holds one procedure (show) or the latest revision of each
// procedure (list).
type ProcedureResult struct {
	Procedure  *procedures.Procedure  `json:"procedure,omitempty"`
	Procedures []procedures.Procedure `json:"procedures,omitempty"`
}

type ResourceShowInput struct {
	ID       string `json:"id,omitempty" jsonschema:"omit to list"`
	Revision int    `json:"revision,omitempty"`
	Notes    int    `json:"notes,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Status   string `json:"status,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// ResourceShowResult holds one resource with notes (show) or the latest
// revision of each resource (list).
type ResourceShowResult struct {
	Detail    *resource.Detail    `json:"detail,omitempty"`
	Resources []resource.Resource `json:"resources,omitempty"`
}

type PinInput struct {
	Target    string `json:"target,omitempty" jsonschema:"resource:, procedure:, decision:, or topic:ID; omit to list"`
	Scope     string `json:"scope,omitempty" jsonschema:"project (default), topic:, roadmap_node:, or task:ID"`
	Unpin     bool   `json:"unpin,omitempty"`
	Creator   string `json:"creator,omitempty"`
	Authority string `json:"authority,omitempty"`
	Task      string `json:"task,omitempty" jsonschema:"list the pins this task's bootstrap shows"`
}

// LeaseInput acquires, renews, or releases one local lease.
type LeaseInput struct {
	Action     string `json:"action" jsonschema:"acquire, renew, or release"`
	Scope      string `json:"scope"`
	Holder     string `json:"holder"`
	Session    string `json:"session,omitempty"`
	Token      int64  `json:"token,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"30 to 86400; default 900"`
}

// LeaseResult is the lease after the action. Renewed is set when acquire found
// the caller already holding the scope.
type LeaseResult = lease.Result

// BriefInput names one plan item, or nothing for the items the current next
// action names.
type BriefInput struct {
	Ref string `json:"ref,omitempty" jsonschema:"task:ID, slice label such as 4e or PLAN/ITEM; omit for the next action's items"`
}

// BriefResult holds one brief with its Markdown, or the named items' lines.
type BriefResult struct {
	Task     *brief.TaskBrief `json:"task,omitempty"`
	Brief    *brief.Brief     `json:"brief,omitempty"`
	Markdown string           `json:"markdown,omitempty"`
	Lines    []string         `json:"lines"`
}

// PinResult holds the appended assertion or the current pins.
type PinResult struct {
	Pin  *pin.Pin   `json:"pin,omitempty"`
	Pins []pin.Line `json:"pins,omitempty"`
}

type RoadmapNodeInput struct {
	ID            string   `json:"id"`
	Revision      int      `json:"revision,omitempty"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	Priority      *int     `json:"priority,omitempty" jsonschema:"0 (highest) to 100; default 50"`
	SourcePath    string   `json:"source_path"`
	SourceRef     string   `json:"source_ref,omitempty"`
	Kind          string   `json:"kind,omitempty" jsonschema:"initiative, milestone, work_item (default), or research"`
	TargetDate    string   `json:"target_date,omitempty"`
	TargetRelease string   `json:"target_release,omitempty"`
	TargetHorizon string   `json:"target_horizon,omitempty"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty" jsonschema:"exact evidence references; at most 64"`
}

// RoadmapViewInput selects one read-only roadmap view.
type RoadmapViewInput struct {
	View   string `json:"view,omitempty" jsonschema:"status, overview, tree, coverage, history, render, check, releases, unreleased, changelog, or changelog_check"`
	NodeID string `json:"node_id,omitempty" jsonschema:"node history or release history ID"`
	Path   string `json:"path,omitempty" jsonschema:"check"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// RoadmapViewResult holds the selected view.
type RoadmapViewResult struct {
	Status         *roadmap.Status          `json:"status,omitempty"`
	Overview       *roadmap.Overview        `json:"overview,omitempty"`
	Tree           *roadmap.RoadmapTree     `json:"tree,omitempty"`
	Coverage       *roadmap.Coverage        `json:"coverage,omitempty"`
	History        *roadmap.NodeHistory     `json:"history,omitempty"`
	Render         *roadmap.Projection      `json:"render,omitempty"`
	Check          *roadmap.ProjectionCheck `json:"check,omitempty"`
	Releases       *release.View            `json:"releases,omitempty"`
	Changelog      *release.Projection      `json:"changelog,omitempty"`
	ChangelogCheck *release.ProjectionCheck `json:"changelog_check,omitempty"`
}

type ReleaseRecordInput struct {
	ID          string          `json:"id"`
	Revision    int             `json:"revision,omitempty"`
	Version     string          `json:"version"`
	Title       string          `json:"title"`
	Status      string          `json:"status" jsonschema:"draft, reviewed, published, or superseded"`
	Reviewer    string          `json:"reviewer,omitempty"`
	PublishedAt string          `json:"published_at,omitempty" jsonschema:"RFC3339; published records only"`
	Entries     []release.Entry `json:"entries" jsonschema:"ordered reviewed entries; at most 64"`
}

// WorkInput selects one read-only current-work view.
type WorkInput struct {
	View               string `json:"view,omitempty" jsonschema:"pulse (default), summary, list, item, check, freshness, or inventory"`
	CheckpointID       string `json:"checkpoint_id,omitempty" jsonschema:"pulse, freshness"`
	TaskID             string `json:"task_id,omitempty" jsonschema:"pulse, freshness"`
	SessionID          string `json:"session_id,omitempty" jsonschema:"pulse, freshness"`
	List               string `json:"list,omitempty" jsonschema:"current (default), active, next, blocked, stale, all, or everything"`
	Identifier         string `json:"identifier,omitempty" jsonschema:"item: node or task ID"`
	Scope              string `json:"scope,omitempty" jsonschema:"check: current (default) or all"`
	RemindAfterSeconds int64  `json:"remind_after_seconds,omitempty" jsonschema:"freshness"`
	RemindAfterEvents  int    `json:"remind_after_events,omitempty" jsonschema:"freshness"`
	Filter             string `json:"filter,omitempty" jsonschema:"inventory: all, everything (adds finished unbound tasks), current_focus, active, resumable, blocked, anomalous, or stale"`
	RoadmapNodeID      string `json:"roadmap_node_id,omitempty" jsonschema:"inventory"`
	Offset             int    `json:"offset,omitempty" jsonschema:"inventory"`
	RefLimit           int    `json:"ref_limit,omitempty" jsonschema:"inventory"`
	Limit              int    `json:"limit,omitempty"`
}

func (input WorkInput) selection() checkpoint.Selection {
	return checkpoint.Selection{CheckpointID: input.CheckpointID, TaskID: input.TaskID, SessionID: input.SessionID}
}

// WorkResult holds the selected view.
type WorkResult struct {
	Pulse     *projectpulse.Pulse     `json:"pulse,omitempty"`
	Summary   *workstate.Summary      `json:"summary,omitempty"`
	List      *workstate.List         `json:"list,omitempty"`
	Item      *workstate.Detail       `json:"item,omitempty"`
	Check     *workstate.Check        `json:"check,omitempty"`
	Freshness *checkpoint.Freshness   `json:"freshness,omitempty"`
	Inventory *roadmap.TrackInventory `json:"inventory,omitempty"`
}

type PresenceInput struct {
	SessionID     string `json:"session_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	Checkout      string `json:"checkout,omitempty"`
	Worktree      string `json:"worktree,omitempty"`
	Branch        string `json:"branch,omitempty"`
	State         string `json:"state,omitempty"`
	LiveOnly      bool   `json:"live_only,omitempty"`
	History       bool   `json:"history,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	FocusSession  string `json:"focus_session,omitempty"`
	FocusTask     string `json:"focus_task,omitempty"`
	FocusRoadmap  string `json:"focus_roadmap_node,omitempty"`
	FocusWorktree string `json:"focus_worktree,omitempty"`
	FocusBranch   string `json:"focus_branch,omitempty"`
	Cleanup       bool   `json:"cleanup,omitempty" jsonschema:"inspect linked worktrees without changing them"`
}

type DecisionRecordInput struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Rationale    string          `json:"rationale" jsonschema:"the user's reasons"`
	Disposition  string          `json:"disposition"`
	Actor        string          `json:"actor" jsonschema:"the deciding person"`
	ActorType    string          `json:"actor_type" jsonschema:"human"`
	Authority    string          `json:"authority" jsonschema:"user_reported or caller_asserted"`
	ReviewSource string          `json:"review_source,omitempty" jsonschema:"where the user decided; required for user_reported"`
	SupersedesID string          `json:"supersedes_id,omitempty"`
	Links        []decision.Link `json:"links,omitempty" jsonschema:"up to 16 links: subject, supports, opposes, resulted_in"`
}

type HistorySearchInput struct {
	Query    string   `json:"query,omitempty" jsonschema:"terms are ANDed; trailing * for prefix"`
	Topic    string   `json:"topic,omitempty" jsonschema:"topic ID or alias, or roadmap node ID"`
	Kinds    []string `json:"kinds,omitempty" jsonschema:"claim kinds such as intent or decision"`
	Families []string `json:"families,omitempty"`
	Since    string   `json:"since,omitempty" jsonschema:"RFC 3339"`
	Until    string   `json:"until,omitempty" jsonschema:"RFC 3339, exclusive"`
	Linked   string   `json:"linked,omitempty" jsonschema:"commit:HASH, roadmap_node:ID, file:PATH: narratives anchored to it, decisions linking to it"`
	Limit    int      `json:"limit,omitempty"`
	Offset   int      `json:"offset,omitempty"`
	MaxBytes int      `json:"max_bytes,omitempty"`
}

type HistoryShowInput struct {
	Refs     []string `json:"refs" jsonschema:"1-20 refs such as narrative:ID, decision:ID, checkpoint:ID"`
	MaxBytes int      `json:"max_bytes,omitempty"`
}

// UsageInput selects and groups a usage report.
type UsageInput struct {
	By     string `json:"by,omitempty" jsonschema:"session, day, model (default), client, task, tool, or family"`
	Since  string `json:"since,omitempty" jsonschema:"YYYY-MM-DD or RFC 3339"`
	Until  string `json:"until,omitempty" jsonschema:"exclusive"`
	Days   int    `json:"days,omitempty"`
	Client string `json:"client,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type NarrativeRecordInput struct {
	ID                  string                 `json:"id,omitempty"`
	Title               string                 `json:"title"`
	Trigger             string                 `json:"trigger,omitempty" jsonschema:"meaningful_checkpoint, accepted_decision, completed_roadmap_slice, commit, or manual"`
	OccurredFrom        string                 `json:"occurred_from,omitempty"`
	OccurredTo          string                 `json:"occurred_to,omitempty"`
	Claims              []worknarrative.Claim  `json:"claims"`
	Anchors             []worknarrative.Anchor `json:"anchors,omitempty"`
	Creator             string                 `json:"creator"`
	CreatorInvocationID string                 `json:"creator_invocation_id,omitempty"`
	SupersedesID        string                 `json:"supersedes_id,omitempty"`
	Commit              string                 `json:"commit,omitempty" jsonschema:"needs outcome, intent, verification, and limitation claims; writes the commit's Git note"`
	SkipNote            bool                   `json:"skip_note,omitempty"`
	Repo                string                 `json:"repo,omitempty" jsonschema:"workspace member holding commit"`
}

type ItemCaptureInput struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id,omitempty"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Observed      string   `json:"observed,omitempty"`
	Reproduction  string   `json:"reproduction,omitempty"`
	Environment   string   `json:"environment,omitempty"`
	Severity      string   `json:"severity,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	TaskID        string   `json:"task_id,omitempty"`
	CheckpointID  string   `json:"checkpoint_id,omitempty"`
	RoadmapNodeID string   `json:"roadmap_node_id,omitempty"`
	From          string   `json:"from,omitempty" jsonschema:"work_item:ID to promote, or friction:ID to track"`
	Creator       string   `json:"creator"`
	Model         string   `json:"model,omitempty"`
}

type ItemInput struct {
	Report        bool   `json:"report,omitempty" jsonschema:"prepare bug report"`
	Expected      string `json:"expected,omitempty"`
	Hypothesis    string `json:"hypothesis,omitempty" jsonschema:"unverified"`
	ID            string `json:"id,omitempty" jsonschema:"omit to list"`
	Kind          string `json:"kind,omitempty"`
	Status        string `json:"status,omitempty" jsonschema:"open or a disposition"`
	Tag           string `json:"tag,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Audit         bool   `json:"audit,omitempty" jsonschema:"pending bodies"`
	Friction      bool   `json:"friction,omitempty" jsonschema:"failure/recovery candidates"`
	MinCount      int    `json:"min_count,omitempty"`
	MaxBytes      int    `json:"max_bytes,omitempty"`
}

// ItemResult holds one item, a list page, an audit packet, or a friction report.
type ItemResult struct {
	Report   *workitem.BugReport  `json:"report,omitempty"`
	Item     *workitem.ShowResult `json:"item,omitempty"`
	Page     *workitem.ListPage   `json:"page,omitempty"`
	Audit    *workitem.Audit      `json:"audit,omitempty"`
	Friction *friction.Report     `json:"friction,omitempty"`
}

type ItemDispositionInput struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason"`
	TargetRefs []string `json:"target_refs,omitempty" jsonschema:"required for accepted, integrated, merged"`
	Creator    string   `json:"creator"`
	Model      string   `json:"model,omitempty"`
}

type TrackPlanRecordInput struct {
	BaseRevision  int              `json:"base_revision,omitempty" jsonschema:"exact latest revision; required for autonomous writes"`
	ID            string           `json:"id"`
	Revision      int              `json:"revision,omitempty"`
	RoadmapNodeID string           `json:"roadmap_node_id,omitempty" jsonschema:"required with items"`
	TaskID        string           `json:"task_id,omitempty"`
	Title         string           `json:"title,omitempty" jsonschema:"required with items"`
	Summary       string           `json:"summary,omitempty" jsonschema:"required with items"`
	Status        string           `json:"status"`
	Creator       string           `json:"creator"`
	Model         string           `json:"model,omitempty" jsonschema:"model identity for agent writes"`
	WorkItemIDs   []string         `json:"work_item_ids,omitempty"`
	Items         []trackplan.Item `json:"items,omitempty" jsonschema:"full item set; omit for human review"`
	LeaseHolder   string           `json:"lease_holder,omitempty" jsonschema:"holder of your lease on this scope"`
	LeaseSession  string           `json:"lease_session,omitempty"`
	LeaseToken    int64            `json:"lease_token,omitempty"`
}

type TrackPlanShowInput struct {
	Policy        bool   `json:"policy,omitempty" jsonschema:"read effective policy for roadmap_node_id"`
	ID            string `json:"id,omitempty" jsonschema:"omit to list"`
	Revision      int    `json:"revision,omitempty"`
	History       bool   `json:"history,omitempty"`
	Detail        bool   `json:"detail,omitempty" jsonschema:"include item detail and evidence"`
	Item          string `json:"item,omitempty" jsonschema:"one item in full"`
	Ready         bool   `json:"ready,omitempty" jsonschema:"dependency-ready items"`
	Status        string `json:"status,omitempty"`
	RoadmapNodeID string `json:"roadmap_node_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// TrackPlanResult holds one revision (outlined, in full, or one item), a
// revision chain, the plan list, or ready items.
type TrackPlanResult struct {
	Policy    *trackplan.Rule                 `json:"policy,omitempty"`
	Outline   *trackplan.Outline              `json:"outline,omitempty"`
	Plan      *trackplan.Plan                 `json:"plan,omitempty"`
	Item      *trackplan.Item                 `json:"item,omitempty"`
	Revisions []trackplan.PlanRevisionSummary `json:"revisions,omitempty"`
	History   []trackplan.Plan                `json:"history,omitempty"`
	Plans     []trackplan.PlanSummary         `json:"plans,omitempty"`
	Ready     []trackplan.ReadyItem           `json:"ready,omitempty"`
}

type InfoInput struct {
	Checks              bool     `json:"checks,omitempty" jsonschema:"add the readiness report"`
	Probes              []string `json:"probes,omitempty" jsonschema:"narrative:ID probes"`
	SourceVerifications []string `json:"source_verifications,omitempty"`
	CheckIntegrity      bool     `json:"check_integrity,omitempty"`
}

type InfoOutput struct {
	Health           buildinfo.Health  `json:"health"`
	DatabaseSchema   string            `json:"database_schema"`
	ProcessStartedAt time.Time         `json:"process_started_at"`
	Readiness        *readiness.Report `json:"readiness,omitempty"`
}

// Run serves the Continuity MCP tools over stdio until the client disconnects.
func Run(ctx context.Context, application *app.App) error {
	startedAt := time.Now().UTC()
	defer observeMCPPresence(context.Background(), application, startedAt, "sessionyield")
	return newServerAt(application, startedAt).Run(ctx, &mcp.StdioTransport{})
}

// ToolNames lists the tools the server registers, in the order it lists them,
// by connecting an in-memory client. Nothing is called.
func ToolNames(ctx context.Context, application *app.App) ([]string, error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	session, err := newServer(application).Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "continuity-tool-list"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var names []string
	for tool, err := range client.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		names = append(names, tool.Name)
	}
	return names, nil
}

// addTool registers a tool with a bare object output schema instead of the
// schema inferred from its result type: clients do not show output schemas to
// the model, and the inferred ones were most of every tools/list response.
func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	tool.OutputSchema = &jsonschema.Schema{Type: "object"}
	mcp.AddTool(server, tool, handler)
}

// addWriteTool registers a tool that changes records. It refuses while a newer
// build has opened the store since this process started, so a stale server
// never writes with superseded semantics.
func addWriteTool[In, Out any](server *mcp.Server, application *app.App, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	addTool(server, tool, func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		if err := application.CheckWriter(ctx); err != nil {
			var zero Out
			return nil, zero, err
		}
		return handler(ctx, request, input)
	})
}

// deliveryWindowSlack is how long before this process started a SessionStart
// delivery can belong to its session: clients may run the hook first.
const deliveryWindowSlack = 2 * time.Minute

func newServer(application *app.App) *mcp.Server {
	return newServerAt(application, time.Now().UTC())
}

func newServerAt(application *app.App, startedAt time.Time) *mcp.Server {
	runtimeState := buildinfo.CaptureRuntime()
	server := mcp.NewServer(&mcp.Implementation{Name: "llewm", Version: runtimeState.Build.ApplicationVersion}, nil)
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if err := adoption.CheckActive(config.StateDir(application.Root)); err != nil {
					return nil, err
				}
				observeMCPPresence(ctx, application, startedAt, "toolcall")
			}
			return next(ctx, method, request)
		}
	})
	addTool(server, &mcp.Tool{Name: "continuity_info", Description: "Report the running server's build, MCP API, and database schema. checks, probes, or check_integrity add the operational-readiness report."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input InfoInput) (*mcp.CallToolResult, InfoOutput, error) {
			migration, err := database.LatestMigration(ctx, application.DB)
			if err != nil {
				return nil, InfoOutput{}, err
			}
			output := InfoOutput{Health: runtimeState.CheckInstalled(), DatabaseSchema: migration, ProcessStartedAt: startedAt}
			if input.Checks || input.CheckIntegrity || len(input.Probes) > 0 || len(input.SourceVerifications) > 0 {
				report, err := application.Readiness.Report(ctx, readiness.Input{Runtime: &runtimeState, ProcessStartedAt: &startedAt, Probes: input.Probes, SourceVerifications: input.SourceVerifications, CheckIntegrity: input.CheckIntegrity})
				if err != nil {
					return nil, InfoOutput{}, err
				}
				output.Readiness = &report
			}
			return nil, output, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_procedure", Description: "Return one reviewed procedure (check or runbook), or without id list the latest revision of each."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ProcedureShowInput) (*mcp.CallToolResult, ProcedureResult, error) {
			if input.ID == "" {
				values, err := application.Procedures.List(ctx, application.ProjectID, procedures.ListOptions{Kind: input.Kind, Status: input.Status, Limit: input.Limit})
				return nil, ProcedureResult{Procedures: values}, err
			}
			value, err := application.Procedures.Latest(ctx, application.ProjectID, input.ID)
			if input.Revision > 0 {
				value, err = application.Procedures.Get(ctx, application.ProjectID, input.ID, input.Revision)
			}
			if err != nil {
				return nil, ProcedureResult{}, err
			}
			if value == nil {
				return nil, ProcedureResult{}, fmt.Errorf("procedure %q was not found in this project", input.ID)
			}
			return nil, ProcedureResult{Procedure: value}, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_resource", Description: "Return one resource with recent state notes, or without id list resources."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ResourceShowInput) (*mcp.CallToolResult, ResourceShowResult, error) {
			if input.ID == "" {
				values, err := application.Resources.List(ctx, application.ProjectID, input.Kind, input.Status, input.Limit)
				return nil, ResourceShowResult{Resources: values}, err
			}
			detail, err := application.Resources.Show(ctx, application.ProjectID, input.ID, input.Revision, input.Notes)
			if err != nil {
				return nil, ResourceShowResult{}, err
			}
			return nil, ResourceShowResult{Detail: &detail}, nil
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_pin", Description: "Pin a resource, procedure, decision, or topic to the project, a topic, a roadmap node, or a task so the bootstrap shows it there; unpin to remove. Without a target, list pins."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input PinInput) (*mcp.CallToolResult, PinResult, error) {
			if input.Target == "" {
				lines, err := pin.Current(ctx, application.DB, application.ProjectID)
				if input.Task != "" {
					lines, err = pin.ForTask(ctx, application.DB, application.ProjectID, input.Task)
				}
				return nil, PinResult{Pins: lines}, err
			}
			targetKind, targetID, err := pin.ParseRef(input.Target)
			if err != nil {
				return nil, PinResult{}, err
			}
			scopeKind, scopeID := "project", ""
			if input.Scope != "" && input.Scope != "project" {
				if scopeKind, scopeID, err = pin.ParseRef(input.Scope); err != nil {
					return nil, PinResult{}, err
				}
			}
			action := "pin"
			if input.Unpin {
				action = "unpin"
			}
			value, err := application.Pins.Set(ctx, pin.Pin{ProjectID: application.ProjectID, TargetKind: targetKind, TargetID: targetID, ScopeKind: scopeKind, ScopeID: scopeID, Action: action, Creator: input.Creator, Authority: input.Authority})
			if err != nil {
				return nil, PinResult{}, err
			}
			return nil, PinResult{Pin: &value}, nil
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_lease", Description: "Acquire, renew, or release an advisory lease saying which agent works on a task, roadmap node, or track plan; cross-machine when the project sets [leases] remote. Acquire fails naming the holder when someone else holds it; renew and release need the token."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input LeaseInput) (*mcp.CallToolResult, LeaseResult, error) {
			kind, id, err := lease.ParseScope(input.Scope)
			if err != nil {
				return nil, LeaseResult{}, err
			}
			request := lease.Request{ProjectID: application.ProjectID, ScopeKind: kind, ScopeID: id, Holder: input.Holder, SessionID: input.Session, Token: input.Token, TTL: time.Duration(input.TTLSeconds) * time.Second}
			switch input.Action {
			case "acquire":
				result, err := application.Leases.Acquire(ctx, request)
				return nil, result, err
			case "renew":
				result, err := application.Leases.Renew(ctx, request)
				return nil, result, err
			case "release":
				result, err := application.Leases.Release(ctx, request)
				return nil, result, err
			}
			return nil, LeaseResult{}, fmt.Errorf("lease action must be acquire, renew, or release, not %q", input.Action)
		})
	addTool(server, &mcp.Tool{Name: "continuity_brief", Description: "Compact task:ID brief: saved state, verification, attempts, decisions and named items. Plan-item refs return goal, dependencies, paths and procedures. Without ref, one-line next-action items."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input BriefInput) (*mcp.CallToolResult, BriefResult, error) {
			if ref := strings.TrimSpace(input.Ref); ref != "" {
				if taskID, ok := strings.CutPrefix(ref, "task:"); ok {
					value, err := brief.BuildTask(ctx, application.DB, application.ProjectID, taskID)
					return nil, BriefResult{Task: &value, Lines: []string{}}, err
				}
				value, err := brief.Build(ctx, application.DB, application.ProjectID, ref)
				if err != nil {
					return nil, BriefResult{}, err
				}
				return nil, BriefResult{Brief: &value, Markdown: brief.Markdown(value), Lines: []string{}}, nil
			}
			latest, err := application.Checkpoints.LatestSelected(ctx, application.ProjectID, checkpoint.Selection{})
			if err != nil || latest == nil {
				return nil, BriefResult{Lines: []string{}}, err
			}
			pending, err := application.Checkpoints.PendingTexts(ctx, application.ProjectID)
			if err != nil {
				return nil, BriefResult{}, err
			}
			items, err := brief.MentionedOrPending(ctx, application.DB, application.ProjectID, latest.NextAction, pending)
			if err != nil {
				return nil, BriefResult{}, err
			}
			result := BriefResult{Lines: []string{}}
			for _, item := range items {
				result.Lines = append(result.Lines, brief.Line(item))
			}
			return nil, result, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_usage", Description: "Report this project's model usage from Claude Code and Codex session files: exact captured tokens and estimated API-rate-equivalent cost with applied rate-card provenance, grouped by session, day, model, client, task, tool, or tool family, with tool-result sizes and session start prompt sizes."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input UsageInput) (*mcp.CallToolResult, usage.Report, error) {
			options := usage.Options{By: input.By, Client: input.Client, Limit: input.Limit}
			for target, value := range map[*time.Time]string{&options.Since: input.Since, &options.Until: input.Until} {
				if value == "" {
					continue
				}
				at, err := time.ParseInLocation("2006-01-02", value, time.Local)
				if err != nil {
					if at, err = time.Parse(time.RFC3339, value); err != nil {
						return nil, usage.Report{}, fmt.Errorf("time %q must be YYYY-MM-DD or RFC 3339", value)
					}
				}
				*target = at
			}
			if input.Days > 0 {
				options.Since = time.Now().AddDate(0, 0, -input.Days)
			}
			report, err := usage.Reporter{DB: application.DB, ProjectID: application.ProjectID, Prices: usage.Prices{Overrides: application.Config.Usage.Prices}}.Report(ctx, options)
			return nil, report, err
		})
	addTool(server, &mcp.Tool{Name: "continuity_work", Description: "Read current work. view: pulse (default; routing for the selection), summary, list (tracks), item (one node or task), check (contradictions among records), freshness (checkpoint age, activity), or inventory (nodes with tasks, checkpoints, actions, anomalies)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input WorkInput) (*mcp.CallToolResult, WorkResult, error) {
			var result WorkResult
			var err error
			switch input.View {
			case "", "pulse":
				var value projectpulse.Pulse
				value, err = application.Pulse.NextSelected(ctx, application.ProjectID, input.selection())
				result.Pulse = &value
			case "summary":
				var value workstate.Summary
				value, err = application.Work.Summary(ctx, application.ProjectID, input.Limit)
				result.Summary = &value
			case "list":
				var value workstate.List
				value, err = application.Work.List(ctx, application.ProjectID, input.List, input.Limit)
				result.List = &value
			case "item":
				var value workstate.Detail
				value, err = application.Work.Show(ctx, application.ProjectID, input.Identifier)
				result.Item = &value
			case "check":
				var value workstate.Check
				value, err = application.Work.Check(ctx, application.ProjectID, input.Scope, input.Limit)
				result.Check = &value
			case "freshness":
				var value checkpoint.Freshness
				value, err = application.Checkpoints.Freshness(ctx, application.ProjectID, input.selection(), input.Limit, checkpoint.ReminderPolicy{MaxAgeSeconds: input.RemindAfterSeconds, MaxEvents: input.RemindAfterEvents})
				result.Freshness = &value
			case "inventory":
				var value roadmap.TrackInventory
				value, err = application.Roadmap.TrackInventory(ctx, application.ProjectID, roadmap.TrackInventoryOptions{Filter: input.Filter, RoadmapNodeID: input.RoadmapNodeID, Offset: input.Offset, Limit: input.Limit, RefLimit: input.RefLimit})
				result.Inventory = &value
			default:
				return nil, WorkResult{}, fmt.Errorf("work view must be pulse, summary, list, item, check, freshness, or inventory, not %q", input.View)
			}
			if err != nil {
				return nil, WorkResult{}, err
			}
			return nil, result, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_presence", Description: "Read bounded device-local live session presence, exact scope overlap, and optional non-mutating worktree cleanup candidates. Presence is distinct from tasks and leases."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input PresenceInput) (*mcp.CallToolResult, presence.Snapshot, error) {
			now := time.Now().UTC()
			report, err := application.Presence.List(ctx, application.ProjectID, presence.Query{
				SessionID: input.SessionID, TaskID: input.TaskID, RoadmapNode: input.RoadmapNodeID,
				CheckoutPath: input.Checkout, WorktreePath: input.Worktree, Branch: input.Branch,
				State: input.State, LiveOnly: input.LiveOnly, History: input.History, Limit: input.Limit, Now: now,
				FocusSession: input.FocusSession, FocusTask: input.FocusTask, FocusRoadmap: input.FocusRoadmap,
				FocusWorktree: input.FocusWorktree, FocusBranch: input.FocusBranch,
			})
			if err != nil {
				return nil, presence.Snapshot{}, err
			}
			result := presence.Snapshot{Report: report}
			if input.Cleanup {
				result.Worktrees = presence.CleanupCandidates(ctx, application.Repositories, report.Presence, now)
			}
			return nil, result, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_bootstrap", Description: "Return the continuity bootstrap for the project. The SessionStart hook already delivers it; with no selection, an unchanged packet returns a one-line notice unless full is set."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input BootstrapInput) (*mcp.CallToolResult, BootstrapOutput, error) {
			selection := checkpoint.Selection{CheckpointID: input.CheckpointID, TaskID: input.TaskID, SessionID: input.SessionID}
			packet, err := application.Bootstrap.BuildForSession(ctx, application.ProjectID, application.Root, application.Config.Project.Name, selection, input.CurrentSessionID)
			if err != nil {
				return nil, BootstrapOutput{}, err
			}
			health := runtimeState.CheckInstalled()
			warning := ""
			if health.RestartRequired {
				warning = "Continuity MCP is stale; restart the client so it launches the installed executable."
			}
			markdown := bootstrap.Markdown(packet)
			if !input.Full && selection == (checkpoint.Selection{}) {
				// The client starts this process with its session, so the session's
				// SessionStart delivery falls shortly before or after startedAt.
				deliveries, err := bootstrap.DeliveredSince(ctx, application.DB, application.ProjectID, input.CurrentSessionID, startedAt.Add(-deliveryWindowSlack))
				if err != nil {
					return nil, BootstrapOutput{}, err
				}
				if delivery, unchanged := bootstrap.DeliveredUnchanged(deliveries, bootstrap.PacketHash(markdown)); unchanged {
					notice := fmt.Sprintf("Unchanged since this session's SessionStart bootstrap (delivered %s, checkpoint %s); it is already in your context. Call again with full: true if it no longer is, for example after compaction.", delivery.DeliveredAt.Format(time.RFC3339), cmp.Or(delivery.CheckpointID, "none"))
					return nil, BootstrapOutput{Markdown: notice, Unchanged: true, RuntimeWarning: warning}, nil
				}
			}
			return nil, BootstrapOutput{Markdown: markdown, RuntimeWarning: warning}, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_checkpoint", Description: "Save a task handoff (the next session's bootstrap). Open predecessor actions must be completed, superseded, or carried forward; dry_run checks the budget without saving. If a newer incompatible writer requires restart, the structured handoff is staged device-locally and returned as deferred recovery."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointInput) (*mcp.CallToolResult, CheckpointOutput, error) {
			value := checkpoint.Checkpoint{
				SessionID: input.SessionID, TaskID: input.TaskID, PredecessorCheckpointID: input.PredecessorCheckpointID, ReviewsTaskID: input.ReviewsTaskID, RequestKey: input.RequestKey, Task: input.Task,
				Status: input.Status, Summary: input.Summary, NextAction: input.NextAction,
				Completed: input.Completed, Changes: input.Changes, Verified: input.Verified, Attempts: input.Attempts, EvidenceRefs: input.EvidenceRefs,
				Fence: lease.Fence{Claim: lease.Claim{Holder: input.LeaseHolder, SessionID: input.LeaseSession, Token: input.LeaseToken}},
			}
			reconciliation := checkpoint.ActionReconciliation{Updates: input.ActionUpdates, CarryForwardActionIDs: input.CarryForwardActionIDs}
			if input.DryRun {
				result, err := application.Bootstrap.PreflightWithReconciliation(ctx, application.ProjectID, application.Root, application.Config.Project.Name, value, reconciliation)
				if err != nil {
					return nil, CheckpointOutput{}, err
				}
				return nil, CheckpointOutput{Preflight: &result, ResearchCapture: application.ResearchCapture(ctx, value)}, nil
			}
			if err := application.CheckWriter(ctx); err != nil {
				var stale *database.StaleWriterError
				if !errors.As(err, &stale) {
					return nil, CheckpointOutput{}, err
				}
				recovery, stageErr := application.StageCheckpointRecovery(value, reconciliation)
				if stageErr != nil {
					return nil, CheckpointOutput{}, fmt.Errorf("%w; checkpoint recovery staging also failed: %v", err, stageErr)
				}
				return nil, CheckpointOutput{Deferred: true, RecoveryID: recovery.ID, RecoveryNotice: "Checkpoint not yet saved. Restart the client, then run `llewm checkpoint recovery apply " + recovery.ID + "` and verify readback."}, nil
			}
			created, err := application.SaveCheckpointWithReconciliation(ctx, value, reconciliation)
			if err != nil {
				var stale *database.StaleWriterError
				if errors.As(err, &stale) {
					recovery, stageErr := application.StageCheckpointRecovery(value, reconciliation)
					if stageErr != nil {
						return nil, CheckpointOutput{}, fmt.Errorf("%w; checkpoint recovery staging also failed: %v", err, stageErr)
					}
					return nil, CheckpointOutput{Deferred: true, RecoveryID: recovery.ID, RecoveryNotice: "Checkpoint not yet saved. Restart the client, then run `llewm checkpoint recovery apply " + recovery.ID + "` and verify readback."}, nil
				}
				return nil, CheckpointOutput{}, err
			}
			return nil, CheckpointOutput{ID: created.Checkpoint.ID, Lease: created.Lease, SessionLease: created.SessionLease, CarriedForward: created.CarriedForward, Reconciled: created.Reconciled, ActionCreated: created.ActionCreated, UnnarratedCommits: application.UnnarratedCommits(ctx), ResearchCapture: application.ResearchCapture(ctx, value)}, nil
		})
	addTool(server, &mcp.Tool{Name: "continuity_checkpoint_actions", Description: "List a task's unresolved checkpoint actions; without task_id or all, refuses when several tasks have actions."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointActionsInput) (*mcp.CallToolResult, CheckpointActionsOutput, error) {
			actions, err := application.Checkpoints.ListActions(ctx, application.ProjectID, input.TaskID, input.All)
			return nil, CheckpointActionsOutput{Actions: actions}, err
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_checkpoint_action_update", Description: "Mark one checkpoint action completed or superseded."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointActionInput) (*mcp.CallToolResult, Receipt, error) {
			action, err := application.Checkpoints.TransitionAction(ctx, application.ProjectID, input.TaskID, input.ID, input.Status, input.Reason)
			return nil, actionReceipt(action), err
		})
	addTool(server, &mcp.Tool{Name: "continuity_checkpoint_get", Description: "Read earlier handoffs: with id, export one checkpoint; with query, find checkpoints by literal text."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input CheckpointGetInput) (*mcp.CallToolResult, CheckpointGetResult, error) {
			if (input.ID == "") == (input.Query == "") {
				return nil, CheckpointGetResult{}, errors.New("give exactly one of id and query")
			}
			if input.Query != "" {
				found, err := application.Checkpoints.Find(ctx, application.ProjectID, checkpoint.FindRequest{Query: input.Query, Limit: input.Limit, Cursor: input.Cursor})
				if err != nil {
					return nil, CheckpointGetResult{}, err
				}
				return nil, CheckpointGetResult{Matches: &found}, nil
			}
			export, err := application.Checkpoints.Get(ctx, application.ProjectID, input.ID, input.MaxBytes)
			if err != nil {
				return nil, CheckpointGetResult{}, err
			}
			if export == nil {
				return nil, CheckpointGetResult{}, fmt.Errorf("checkpoint %q not found in this project", input.ID)
			}
			return nil, CheckpointGetResult{Checkpoint: export}, nil
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_roadmap_node", Description: "Record a roadmap node revision."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input RoadmapNodeInput) (*mcp.CallToolResult, Receipt, error) {
			priority := 50
			if input.Priority != nil {
				priority = *input.Priority
			}
			node, err := application.Roadmap.AppendNode(ctx, roadmap.Node{ProjectID: application.ProjectID, ID: input.ID, Revision: input.Revision, Title: input.Title, Status: input.Status, Priority: priority, Kind: input.Kind, TargetDate: input.TargetDate, TargetRelease: input.TargetRelease, TargetHorizon: input.TargetHorizon, EvidenceRefs: input.EvidenceRefs, SourcePath: input.SourcePath, SourceRef: input.SourceRef})
			return nil, roadmapNodeReceipt(node), err
		})
	addTool(server, &mcp.Tool{Name: "continuity_roadmap_view", Description: "Read deterministic roadmap, release, and changelog views; rendering and checks never write or publish."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input RoadmapViewInput) (*mcp.CallToolResult, RoadmapViewResult, error) {
			var result RoadmapViewResult
			var err error
			switch input.View {
			case "", "status":
				var value roadmap.Status
				value, err = application.Roadmap.Status(ctx, application.ProjectID)
				result.Status = &value
			case "overview":
				var value roadmap.Overview
				value, err = application.Roadmap.Overview(ctx, application.ProjectID, input.Limit)
				result.Overview = &value
			case "tree":
				var value roadmap.RoadmapTree
				value, err = application.Roadmap.Tree(ctx, application.ProjectID, roadmap.TreeOptions{Offset: input.Offset, Limit: input.Limit})
				result.Tree = &value
			case "coverage":
				var value roadmap.Coverage
				value, err = application.Roadmap.Coverage(ctx, application.ProjectID, roadmap.CoverageOptions{Offset: input.Offset, Limit: input.Limit})
				result.Coverage = &value
			case "history":
				var value roadmap.NodeHistory
				value, err = application.Roadmap.NodeHistory(ctx, application.ProjectID, roadmap.NodeHistoryOptions{NodeID: input.NodeID, Offset: input.Offset, Limit: input.Limit})
				result.History = &value
			case "render":
				var value roadmap.Projection
				value, err = application.Roadmap.Render(ctx, application.ProjectID)
				result.Render = &value
			case "check":
				var value roadmap.ProjectionCheck
				value, err = application.Roadmap.CheckProjection(ctx, application.ProjectID, input.Path)
				result.Check = &value
			case "releases":
				var value release.View
				value, err = application.Releases.History(ctx, application.ProjectID, input.NodeID, input.Limit)
				result.Releases = &value
			case "unreleased":
				var value release.View
				value, err = application.Releases.History(ctx, application.ProjectID, "unreleased", 1)
				result.Releases = &value
			case "changelog":
				var value release.Projection
				value, err = application.Releases.Render(ctx, application.ProjectID)
				result.Changelog = &value
			case "changelog_check":
				var value release.ProjectionCheck
				value, err = application.Releases.Check(ctx, application.ProjectID, input.Path)
				result.ChangelogCheck = &value
			default:
				return nil, RoadmapViewResult{}, fmt.Errorf("unsupported roadmap view %q", input.View)
			}
			if err != nil {
				return nil, RoadmapViewResult{}, err
			}
			return nil, result, nil
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_release_record", Description: "Append a release revision. Published content must match its latest review; nothing is published externally."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input ReleaseRecordInput) (*mcp.CallToolResult, release.Record, error) {
			publishedAt, err := optionalRFC3339(input.PublishedAt)
			if err != nil {
				return nil, release.Record{}, fmt.Errorf("published_at: %w", err)
			}
			value, err := application.Releases.Append(ctx, release.Record{ProjectID: application.ProjectID, ID: input.ID, Revision: input.Revision, Version: input.Version, Title: input.Title, Status: input.Status, Reviewer: input.Reviewer, PublishedAt: publishedAt, Entries: input.Entries})
			return nil, value, err
		})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_decision_record", Description: "Record a decision the user explicitly made, with links to what it concerns."}, func(ctx context.Context, _ *mcp.CallToolRequest, input DecisionRecordInput) (*mcp.CallToolResult, Receipt, error) {
		value, err := application.Decisions.Append(ctx, decision.Record{ID: input.ID, ProjectID: application.ProjectID, Title: input.Title, Rationale: input.Rationale, Disposition: input.Disposition, Actor: input.Actor, ActorType: input.ActorType, Authority: input.Authority, ReviewSource: input.ReviewSource, SupersedesID: input.SupersedesID, Links: input.Links})
		return nil, decisionReceipt(value), err
	})
	addTool(server, &mcp.Tool{Name: "continuity_history_search", Description: "First discovery step before diagnosing or redesigning existing behavior: search saved intent, decisions, attempts, and outcomes with focused terms. Returns bounded metadata and snippets; expand relevant refs with continuity_history_show, then verify current evidence. No useful match means proceed to code."}, func(ctx context.Context, _ *mcp.CallToolRequest, input HistorySearchInput) (*mcp.CallToolResult, history.SearchPage, error) {
		page, err := application.History.Search(ctx, application.ProjectID, history.SearchOptions{Query: input.Query, Topic: input.Topic, Kinds: input.Kinds, Families: input.Families, Since: input.Since, Until: input.Until, Linked: input.Linked, Limit: input.Limit, Offset: input.Offset, MaxBytes: input.MaxBytes})
		return nil, page, err
	})
	addTool(server, &mcp.Tool{Name: "continuity_history_show", Description: "Expand exact history references within a byte budget."}, func(ctx context.Context, _ *mcp.CallToolRequest, input HistoryShowInput) (*mcp.CallToolResult, history.ShowPage, error) {
		page, err := application.History.Show(ctx, application.ProjectID, input.Refs, input.MaxBytes)
		return nil, page, err
	})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_narrative_record", Description: "Record a work narrative: ordered claims with authority, plus anchors. With commit, it is the commit's record and writes its Git note."}, func(ctx context.Context, _ *mcp.CallToolRequest, input NarrativeRecordInput) (*mcp.CallToolResult, Receipt, error) {
		from, err := optionalRFC3339(input.OccurredFrom)
		if err != nil {
			return nil, Receipt{}, fmt.Errorf("occurred_from: %w", err)
		}
		to, err := optionalRFC3339(input.OccurredTo)
		if err != nil {
			return nil, Receipt{}, fmt.Errorf("occurred_to: %w", err)
		}
		value := worknarrative.Record{ID: input.ID, ProjectID: application.ProjectID, Title: input.Title, Trigger: input.Trigger, OccurredFrom: from, OccurredTo: to, Claims: input.Claims, Anchors: input.Anchors, Creator: input.Creator, CreatorInvocationID: input.CreatorInvocationID, SupersedesID: input.SupersedesID}
		if input.Commit == "" {
			record, err := application.Narratives.Append(ctx, value)
			return nil, narrativeReceipt(worknarrative.RecordResult{Record: record}), err
		}
		record, note, err := application.Narratives.RecordCommit(ctx, value, input.Commit, input.Repo, input.SkipNote)
		return nil, narrativeReceipt(worknarrative.RecordResult{Record: record, Note: &note}), err
	})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_track_plan_record", Description: "Record revisions under progress/delegation policy. Without items, record explicit human review."}, func(ctx context.Context, _ *mcp.CallToolRequest, input TrackPlanRecordInput) (*mcp.CallToolResult, Receipt, error) {
		if input.Items == nil {
			value, err := application.AcceptPlan(ctx, input.ID, input.Status, input.Creator, lease.Claim{Holder: input.LeaseHolder, SessionID: input.LeaseSession, Token: input.LeaseToken})
			return nil, planReceipt(value), err
		}
		value, err := application.AppendPlan(ctx, trackplan.Plan{Fence: lease.Fence{Claim: lease.Claim{Holder: input.LeaseHolder, SessionID: input.LeaseSession, Token: input.LeaseToken}}, ProjectID: application.ProjectID, ID: input.ID, Revision: input.Revision, BaseRevision: input.BaseRevision, RoadmapNodeID: input.RoadmapNodeID, TaskID: input.TaskID, Title: input.Title, Summary: input.Summary, Status: input.Status, Creator: input.Creator, Model: input.Model, WorkItemIDs: input.WorkItemIDs, Items: input.Items})
		return nil, planReceipt(value), err
	})
	addTool(server, &mcp.Tool{Name: "continuity_track_plan", Description: "Read plans: outline by default; detail/item/history expand. Without id list plans; ready reads dependency-ready items; policy reads delegation."}, func(ctx context.Context, _ *mcp.CallToolRequest, input TrackPlanShowInput) (*mcp.CallToolResult, TrackPlanResult, error) {
		if input.Policy {
			if input.ID != "" || input.Ready || input.History || input.Item != "" {
				return nil, TrackPlanResult{}, fmt.Errorf("policy read does not select plan content")
			}
			value := application.TrackPlans.Policy.For(input.RoadmapNodeID)
			return nil, TrackPlanResult{Policy: &value}, nil
		}
		options := trackplan.PlanListOptions{Status: input.Status, RoadmapNodeID: input.RoadmapNodeID, TaskID: input.TaskID, Limit: input.Limit}
		switch {
		case input.Ready:
			values, err := application.TrackPlans.Ready(ctx, application.ProjectID, trackplan.PlanListOptions{RoadmapNodeID: input.RoadmapNodeID, TaskID: input.TaskID, Limit: input.Limit})
			return nil, TrackPlanResult{Ready: values}, err
		case input.ID == "":
			values, err := application.TrackPlans.ListPlans(ctx, application.ProjectID, options)
			return nil, TrackPlanResult{Plans: values}, err
		case input.History:
			values, err := application.TrackPlans.PlanHistory(ctx, application.ProjectID, input.ID)
			if err != nil || input.Detail {
				return nil, TrackPlanResult{History: values}, err
			}
			revisions := make([]trackplan.PlanRevisionSummary, 0, len(values))
			for _, value := range values {
				revisions = append(revisions, value.RevisionSummary())
			}
			return nil, TrackPlanResult{Revisions: revisions}, nil
		}
		value, err := application.TrackPlans.Plan(ctx, application.ProjectID, input.ID, input.Revision)
		if err != nil {
			return nil, TrackPlanResult{}, err
		}
		if value == nil {
			return nil, TrackPlanResult{}, fmt.Errorf("track plan %q not found in this project", input.ID)
		}
		switch {
		case input.Item != "":
			item, ok := value.Item(input.Item)
			if !ok {
				return nil, TrackPlanResult{}, fmt.Errorf("plan:%s@%d has no item %q", value.ID, value.Revision, input.Item)
			}
			return nil, TrackPlanResult{Item: &item}, nil
		case input.Detail:
			return nil, TrackPlanResult{Plan: value}, nil
		}
		outline := value.Outline()
		return nil, TrackPlanResult{Outline: &outline}, nil
	})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_item_capture", Description: "Capture a pending idea, bug, task or research question without changing active work."}, func(ctx context.Context, _ *mcp.CallToolRequest, input ItemCaptureInput) (*mcp.CallToolResult, Receipt, error) {
		v, err := application.WorkItems.Capture(ctx, workitem.Capture{Item: workitem.Item{ID: input.ID, ProjectID: application.ProjectID, Kind: input.Kind, Title: input.Title, Body: input.Body, Observed: input.Observed, Reproduction: input.Reproduction, Environment: input.Environment, Severity: input.Severity, Tags: input.Tags, TaskID: input.TaskID, CheckpointID: input.CheckpointID, RoadmapNodeID: input.RoadmapNodeID, Creator: input.Creator, Model: input.Model}, From: input.From})
		return nil, itemReceipt(v), err
	})
	addTool(server, &mcp.Tool{Name: "continuity_item", Description: "Read items: id for one; id+report for a redacted bug draft (no submission). Otherwise list metadata, audit pending bodies, or inspect friction."}, func(ctx context.Context, _ *mcp.CallToolRequest, input ItemInput) (*mcp.CallToolResult, ItemResult, error) {
		if input.Report {
			if input.ID == "" || input.Audit || input.Friction {
				return nil, ItemResult{}, fmt.Errorf("report requires id and cannot be combined with audit or friction")
			}
			v, err := application.WorkItems.Report(ctx, application.ProjectID, input.ID, workitem.ReportOptions{Expected: input.Expected, Hypothesis: input.Hypothesis})
			return nil, ItemResult{Report: v}, err
		}
		if input.Expected != "" || input.Hypothesis != "" {
			return nil, ItemResult{}, fmt.Errorf("expected and hypothesis require report")
		}
		switch {
		case input.ID != "":
			v, err := application.WorkItems.Show(ctx, application.ProjectID, input.ID, input.MaxBytes)
			if err != nil {
				return nil, ItemResult{}, err
			}
			if v == nil {
				return nil, ItemResult{}, fmt.Errorf("work item %q not found in this project", input.ID)
			}
			return nil, ItemResult{Item: v}, nil
		case input.Audit:
			v, err := application.WorkItems.Audit(ctx, application.ProjectID, input.Kind, input.MaxBytes)
			return nil, ItemResult{Audit: &v}, err
		case input.Friction:
			v, err := application.Friction.Report(ctx, application.ProjectID, friction.Options{MinCount: input.MinCount, Limit: input.Limit, MaxBytes: input.MaxBytes})
			return nil, ItemResult{Friction: &v}, err
		}
		v, err := application.WorkItems.ListPage(ctx, application.ProjectID, workitem.ListOptions{Kind: input.Kind, Status: input.Status, Tag: input.Tag, TaskID: input.TaskID, RoadmapNodeID: input.RoadmapNodeID, Limit: input.Limit})
		return nil, ItemResult{Page: &v}, err
	})
	addWriteTool(server, application, &mcp.Tool{Name: "continuity_item_disposition", Description: "Review an item: accepted/deferred stay open; integrated/discarded/merged/answered close it. answered needs research findings."}, func(ctx context.Context, _ *mcp.CallToolRequest, input ItemDispositionInput) (*mcp.CallToolResult, Receipt, error) {
		v, err := application.WorkItems.Dispose(ctx, workitem.Disposition{ProjectID: application.ProjectID, ItemID: input.ID, Status: input.Status, Reason: input.Reason, TargetRefs: input.TargetRefs, Creator: input.Creator, Model: input.Model})
		return nil, dispositionReceipt(v), err
	})
	return server
}

// observeMCPPresence attributes one stdio server to the sole recent SessionStart
// delivery when that association is unambiguous. MCP does not carry the host
// client's Continuity session ID, so ambiguity deliberately records nothing.
func observeMCPPresence(ctx context.Context, application *app.App, startedAt time.Time, eventType string) {
	deliveries, err := bootstrap.DeliveredSince(ctx, application.DB, application.ProjectID, "", startedAt.Add(-deliveryWindowSlack))
	if err != nil || len(deliveries) == 0 {
		return
	}
	newest := deliveries[0]
	for _, delivery := range deliveries[1:] {
		if delivery.Client != newest.Client || delivery.SessionID != newest.SessionID {
			return
		}
	}
	_ = application.ObservePresence(ctx, newest.Client, newest.SessionID, "", eventType, application.Root, time.Now().UTC())
}
