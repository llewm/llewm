package bootstrap

import (
	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/config"

	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/instructions"
	"github.com/llewm/llewm/components/continuity/internal/pin"
)

type Packet struct {
	Trial         bool                   `json:"trial,omitempty"`
	SchemaVersion int                    `json:"schema_version"`
	Project       string                 `json:"project"`
	Global        []instructions.Section `json:"global,omitempty"`
	Rules         []instructions.Section `json:"rules"`
	// CoreRules are Continuity's built-in rules after configuration;
	// CoreRulesDisabled names those configuration turned off.
	CoreRules         []CoreRule             `json:"core_rules,omitempty"`
	CoreRulesDisabled []string               `json:"core_rules_disabled,omitempty"`
	CoreRulesWarning  string                 `json:"core_rules_warning,omitempty"`
	Checkpoint        *checkpoint.Checkpoint `json:"checkpoint,omitempty"`
	UnresolvedActions []checkpoint.Action    `json:"unresolved_actions,omitempty"`
	// OtherTaskActions counts unresolved actions owned by tasks other than the
	// packet's; they are never this task's to reconcile.
	OtherTaskActions int               `json:"other_task_actions,omitempty"`
	Fallback         *RecoveryFallback `json:"fallback,omitempty"`
	Pins             []pin.Line        `json:"pins,omitempty"`
	PinsOmitted      int               `json:"pins_omitted,omitempty"`
	// CodeWarning says when this machine's checkouts lack the code the
	// checkpoint was written on (machine-observed at bootstrap time).
	CodeWarning string `json:"code_warning,omitempty"`
	// ChangeNotice names commits by others since the checkpoint that changed
	// files the task touched (machine-observed at bootstrap time).
	ChangeNotice string `json:"change_notice,omitempty"`
	// OverlapNotice names files the task shares with other in-progress tasks.
	OverlapNotice string `json:"overlap_notice,omitempty"`
	// ReviewNotice names the task this one reviews and tasks reviewing it.
	ReviewNotice string `json:"review_notice,omitempty"`
	reviewShort  string
	// ForkNotice says when the current task, or others, have several checkpoint
	// heads, when record revisions have several versions, and when records were
	// superseded more than once (machine-observed at bootstrap time).
	ForkNotice string `json:"fork_notice,omitempty"`
	// LeaseNotice names who holds the current task's scopes on this machine
	// and counts other held leases (machine-observed at bootstrap time).
	LeaseNotice string `json:"lease_notice,omitempty"`
	// GrantNotice lists the person's active standing grants and counts void
	// ones (machine-observed at bootstrap time).
	GrantNotice string `json:"grant_notice,omitempty"`
	// WorkItemNotice counts open work items by kind (machine-observed at
	// bootstrap time).
	WorkItemNotice string `json:"work_item_notice,omitempty"`
	// ContractNotice names contracts whose marked sections drifted since they
	// were last accepted (machine-observed at compile time), or is empty.
	ContractNotice string `json:"contract_notice,omitempty"`
	// RecoveryNotice reports device-local checkpoint envelopes that still need
	// a current writer to apply or an explicit discard.
	RecoveryNotice string `json:"recovery_notice,omitempty"`
	// Briefs are one-line summaries of the open plan items the next action
	// names by slice label or item:ID, read from plan records.
	Briefs []string `json:"briefs,omitempty"`
	// Choice says which rule chose the task and names other active tasks
	// (machine-observed at bootstrap time).
	Choice *Choice `json:"choice,omitempty"`
	// Detail fields may be deferred to exact checkpoint retrieval when later
	// guidance growth makes a previously valid checkpoint too large to deliver.
	CheckpointDetailsOmitted []string `json:"checkpoint_details_omitted,omitempty"`
	Truncated                bool     `json:"truncated"`
}

type RecoveryFallback struct {
	State             string                           `json:"state"`
	Selection         string                           `json:"selection"`
	EventsObserved    int                              `json:"events_observed"`
	RecentActivity    []checkpoint.ActivityObservation `json:"recent_activity"`
	ActivityTruncated bool                             `json:"activity_truncated"`
	Authority         string                           `json:"authority"`
	Uncertainty       string                           `json:"uncertainty"`
	TargetedQueries   []string                         `json:"targeted_queries"`
}

type Readiness struct {
	SchemaVersion                        int                 `json:"schema_version"`
	Ready                                bool                `json:"ready"`
	MaxBytes                             int                 `json:"max_bytes"`
	JSONBytes                            int                 `json:"json_bytes"`
	MarkdownBytes                        int                 `json:"markdown_bytes"`
	OversizeByBytes                      int                 `json:"oversize_by_bytes"`
	LimitingFormats                      []string            `json:"limiting_formats"`
	BaseBytes                            int                 `json:"base_bytes"`
	GlobalGuidanceBytes                  int                 `json:"global_guidance_bytes"`
	ProjectGuidanceBytes                 int                 `json:"project_guidance_bytes"`
	CheckpointKernelBytes                int                 `json:"checkpoint_kernel_bytes"`
	CheckpointDetailBytes                int                 `json:"checkpoint_detail_bytes"`
	UnresolvedActionBytes                int                 `json:"unresolved_action_bytes"`
	UnresolvedActionCount                int                 `json:"unresolved_action_count"`
	CurrentUnresolvedActionCount         int                 `json:"current_unresolved_action_count"`
	ProjectedUnresolvedActionCount       int                 `json:"projected_unresolved_action_count"`
	ReconciledActionCount                int                 `json:"reconciled_action_count"`
	ExplicitlyCarriedActionCount         int                 `json:"explicitly_carried_action_count"`
	CreatesNextAction                    bool                `json:"creates_next_action"`
	ProjectedPredecessorActions          []checkpoint.Action `json:"projected_predecessor_actions"`
	ProjectedPredecessorActionsTruncated bool                `json:"projected_predecessor_actions_truncated"`
	Authority                            string              `json:"authority"`
	Disclaimer                           string              `json:"disclaimer"`
}

type Compiler struct {
	Checkpoints checkpoint.Service
	MaxBytes    int
	GlobalRoot  string
	// Pins returns the pins relevant to a task (or only project pins when the
	// task is empty). Nil means no pins section.
	Pins func(ctx context.Context, projectID, taskID string) ([]pin.Line, error)
	// CodeCheck compares the code a checkpoint was written on with the local
	// checkouts and returns a warning, or "". Nil means no check. It is
	// best-effort: a failure shows no warning rather than failing the bootstrap.
	CodeCheck func(ctx context.Context, projectID, checkpointID string) string
	// CoreRules are the resolved built-in rules; nil means DefaultCoreRules.
	// CoreRulesDisabled names rules configuration turned off.
	CoreRules         []CoreRule
	CoreRulesDisabled []string
	// CoreRulesWarning reports configuration that was rejected.
	CoreRulesWarning string
	// Coordination reports commits by others since the checkpoint that changed
	// the task's files, and files shared with other in-progress tasks. Nil means
	// no check; like CodeCheck it is best-effort.
	Coordination func(ctx context.Context, projectID string, latest *checkpoint.Checkpoint) (changed, overlap string)
	// Reviews describes review links: the task the checkpoint's task reviews,
	// and tasks that review it, as a full line and a names-only fallback the
	// budget can keep. Nil means none; like CodeCheck it is best-effort.
	Reviews func(ctx context.Context, projectID string, latest *checkpoint.Checkpoint) (full, short string)
	// ForkCheck describes unresolved checkpoint forks, the current task's first,
	// or returns "". Nil means no check; like CodeCheck it is best-effort.
	ForkCheck func(ctx context.Context, projectID, taskID string) string
	// LeaseCheck describes held leases, the current task's first, or returns
	// "". Nil means no check; like CodeCheck it is best-effort.
	LeaseCheck func(ctx context.Context, projectID, taskID string) string
	// GrantCheck describes standing grants, or returns "". Nil means no
	// check; like CodeCheck it is best-effort.
	GrantCheck func(ctx context.Context, projectID string) string
	// WorkItemCheck counts open work items, or returns "". Nil means no
	// check; like CodeCheck it is best-effort.
	WorkItemCheck func(ctx context.Context, projectID string) string
	// ContractCheck describes contract drift, or returns "". Nil means no
	// contract line.
	ContractCheck func(ctx context.Context, projectID string) string
	// RecoveryCheck describes pending device-local checkpoint recovery, or
	// returns "". Nil means no check; it is best-effort.
	RecoveryCheck func(ctx context.Context, projectID string) string
	// Briefs returns one-line briefs for the open plan items a next action
	// names. Nil means none; like CodeCheck it is best-effort.
	Briefs func(ctx context.Context, projectID, nextAction string) []string
	// Choose picks the task when the selection is empty (presence, then current
	// branch, then newest) and lists other active tasks; sessionID is the
	// caller's session, when known, for the presence rule. It also receives an
	// explicit selection, for the other-task list. Nil keeps the project-wide
	// newest checkpoint; like CodeCheck it is best-effort and returns nil on
	// failure.
	Choose func(ctx context.Context, projectID, sessionID string, selection checkpoint.Selection) *Choice
}

// Pinned-section bounds: pins are reminders, so they stay short and are the
// first thing dropped when the whole bootstrap is over budget.
// maxBriefLines bounds the named-slice lines; they are dropped before pins
// when the bootstrap is over budget.
const maxBriefLines = 4

const (
	maxPinLines = 8
	maxPinBytes = 800
)

// pins loads the pins for a task within the section bounds.
func (c Compiler) pins(ctx context.Context, projectID, taskID string) ([]pin.Line, int, error) {
	if c.Pins == nil {
		return nil, 0, nil
	}
	lines, err := c.Pins(ctx, projectID, taskID)
	if err != nil {
		return nil, 0, err
	}
	kept, size := []pin.Line{}, 0
	for _, line := range lines {
		size += len(pinLine(line))
		if len(kept) == maxPinLines || size > maxPinBytes {
			break
		}
		kept = append(kept, line)
	}
	return kept, len(lines) - len(kept), nil
}

func pinLine(line pin.Line) string {
	return fmt.Sprintf("- %s — %s (%s)\n", line.Ref, line.Title, line.Scope)
}

const (
	maxConversationTitleRunes = 96
	maxFallbackActivities     = 3
)

func (c Compiler) Build(ctx context.Context, projectID, root, name string) (Packet, error) {
	return c.BuildSelected(ctx, projectID, root, name, checkpoint.Selection{})
}

// Preflight compiles a prospective checkpoint with the same instruction sources,
// unresolved predecessor actions, serializers, and byte budget as startup delivery.
// It performs no persistence and does not make the prospective checkpoint current.
func (c Compiler) Preflight(ctx context.Context, projectID, root, name string, candidate checkpoint.Checkpoint) (Readiness, error) {
	return c.PreflightWithReconciliation(ctx, projectID, root, name, candidate, checkpoint.ActionReconciliation{})
}

// PreflightWithReconciliation compiles the checkpoint and the exact projected
// predecessor-action state after the requested atomic dispositions.
func (c Compiler) PreflightWithReconciliation(ctx context.Context, projectID, root, name string, candidate checkpoint.Checkpoint, reconciliation checkpoint.ActionReconciliation) (Readiness, error) {
	if c.MaxBytes <= 0 {
		c.MaxBytes = 16_384
	}
	candidate.ProjectID = projectID
	prepared, err := checkpoint.Prepare(candidate)
	if err != nil {
		return Readiness{}, err
	}
	if err := c.Checkpoints.ValidateLineage(ctx, prepared); err != nil {
		return Readiness{}, err
	}
	global, err := instructions.DiscoverGlobal(c.GlobalRoot)
	if err != nil {
		return Readiness{}, err
	}
	rules, err := instructions.Discover(root)
	if err != nil {
		return Readiness{}, err
	}
	projection, err := c.Checkpoints.ProjectActions(ctx, projectID, prepared.TaskID, prepared.Status, prepared.PredecessorCheckpointID, reconciliation)
	if err != nil {
		return Readiness{}, err
	}
	// Persistence carries every remaining predecessor to the new checkpoint.
	// Account for the new status and carried-to ID, not the pre-save rows.
	unresolved := append([]checkpoint.Action(nil), projection.Remaining...)
	for i := range unresolved {
		unresolved[i].Status = "carried_forward"
		unresolved[i].CarriedToCheckpoint = prepared.ID
	}
	projectedActions := unresolved
	projectedActionsTruncated := false
	if len(projectedActions) > 64 {
		projectedActions = projectedActions[:64]
		projectedActionsTruncated = true
	}
	pins, omitted, err := c.pins(ctx, projectID, prepared.TaskID)
	if err != nil {
		return Readiness{}, err
	}
	phase, err := adoption.Load(config.StateDir(root))
	if err != nil {
		return Readiness{}, err
	}
	packet := Packet{Trial: phase.Phase() == "trial", SchemaVersion: 3, Project: name, Global: global, Rules: rules, CoreRules: c.coreRules(), CoreRulesDisabled: c.CoreRulesDisabled, CoreRulesWarning: c.CoreRulesWarning, Checkpoint: &prepared, UnresolvedActions: unresolved, Pins: pins, PinsOmitted: omitted}
	c.addNotices(ctx, projectID, &packet)
	var automaticChoice *Choice
	if c.Choose != nil {
		selection := checkpoint.Selection{TaskID: prepared.TaskID}
		if prepared.TaskID == "" {
			selection.CheckpointID = prepared.ID
		}
		packet.Choice = c.Choose(ctx, projectID, prepared.SessionID, selection)
		automaticChoice = c.Choose(ctx, projectID, prepared.SessionID, checkpoint.Selection{})
	}
	body, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return Readiness{}, err
	}
	jsonBytes, markdownBytes := len(body)+1, len([]byte(Markdown(packet)))
	// Explicit readback and normal startup may render different selection
	// notices. Reserve the larger current representation in each format.
	if automaticChoice != nil {
		automatic := packet
		automatic.Choice = automaticChoice
		body, err := json.MarshalIndent(automatic, "", "  ")
		if err != nil {
			return Readiness{}, err
		}
		jsonBytes = max(jsonBytes, len(body)+1)
		markdownBytes = max(markdownBytes, len(Markdown(automatic)))
	}
	result := Readiness{
		SchemaVersion: 1, MaxBytes: c.MaxBytes, JSONBytes: jsonBytes, MarkdownBytes: markdownBytes,
		UnresolvedActionCount: len(unresolved), CurrentUnresolvedActionCount: len(projection.Current),
		ProjectedUnresolvedActionCount: projection.ProjectedUnresolvedCount, ReconciledActionCount: len(projection.Reconciled),
		ExplicitlyCarriedActionCount: len(projection.ExplicitlyCarried), CreatesNextAction: projection.CreatesNextAction,
		ProjectedPredecessorActions: projectedActions, ProjectedPredecessorActionsTruncated: projectedActionsTruncated, Authority: "prospective_caller_asserted",
		Disclaimer: "Read-only prospective compilation. Byte counts and projected actions use the current instruction sources, unresolved predecessors, and requested lifecycle dispositions; this does not save a checkpoint, make it current, or authorize its next action.",
	}
	base := Packet{SchemaVersion: 3, Project: name}
	baseBytes := len([]byte(Markdown(base)))
	result.BaseBytes = baseBytes
	result.GlobalGuidanceBytes = len([]byte(Markdown(Packet{SchemaVersion: 3, Project: name, Global: global}))) - baseBytes
	result.ProjectGuidanceBytes = len([]byte(Markdown(Packet{SchemaVersion: 3, Project: name, Rules: rules}))) - baseBytes
	kernel := prepared
	kernel.Completed, kernel.Changes, kernel.Verified, kernel.EvidenceRefs, kernel.Attempts = nil, nil, nil, nil, nil
	kernelPacket := Packet{SchemaVersion: 3, Project: name, Checkpoint: &kernel}
	result.CheckpointKernelBytes = len([]byte(Markdown(kernelPacket))) - baseBytes
	fullCheckpointPacket := Packet{SchemaVersion: 3, Project: name, Checkpoint: &prepared}
	result.CheckpointDetailBytes = len([]byte(Markdown(fullCheckpointPacket))) - len([]byte(Markdown(kernelPacket)))
	withActions := fullCheckpointPacket
	withActions.UnresolvedActions = unresolved
	result.UnresolvedActionBytes = len([]byte(Markdown(withActions))) - len([]byte(Markdown(fullCheckpointPacket)))
	// Base includes the current notices, selection, pins and named slices.
	// Keep all Markdown costs accounted for without hiding metadata overhead.
	result.BaseBytes += markdownBytes - (result.BaseBytes + result.GlobalGuidanceBytes + result.ProjectGuidanceBytes + result.CheckpointKernelBytes + result.CheckpointDetailBytes + result.UnresolvedActionBytes)
	result.Ready = jsonBytes <= c.MaxBytes && markdownBytes <= c.MaxBytes
	if jsonBytes > c.MaxBytes {
		result.LimitingFormats = append(result.LimitingFormats, "json")
		result.OversizeByBytes = jsonBytes - c.MaxBytes
	}
	if markdownBytes > c.MaxBytes {
		result.LimitingFormats = append(result.LimitingFormats, "markdown")
		if over := markdownBytes - c.MaxBytes; over > result.OversizeByBytes {
			result.OversizeByBytes = over
		}
	}
	return result, nil
}

func (c Compiler) BuildSelected(ctx context.Context, projectID, root, name string, selection checkpoint.Selection) (Packet, error) {
	return c.BuildForSession(ctx, projectID, root, name, selection, "")
}

// BuildForSession compiles the bootstrap for a caller session: with an empty
// selection, the task where that session has live presence comes first.
func (c Compiler) BuildForSession(ctx context.Context, projectID, root, name string, selection checkpoint.Selection, sessionID string) (Packet, error) {
	phase, err := adoption.Load(config.StateDir(root))
	if err != nil {
		return Packet{}, err
	}
	if phase.Phase() == "exited" {
		return Packet{}, fmt.Errorf("Continuity %s: existing project prose controls work; automatic task bootstrap is disabled", phase.Phase())
	}

	if c.MaxBytes <= 0 {
		c.MaxBytes = 16_384
	}
	global, err := instructions.DiscoverGlobal(c.GlobalRoot)
	if err != nil {
		return Packet{}, err
	}
	rules, err := instructions.Discover(root)
	if err != nil {
		return Packet{}, err
	}
	var choice *Choice
	if c.Choose != nil {
		if choice = c.Choose(ctx, projectID, strings.TrimSpace(sessionID), selection); choice != nil {
			selection = choice.Selection
		}
	}
	ambiguous := choice != nil && choice.Rule == RuleAmbiguous
	var latest *checkpoint.Checkpoint
	if !ambiguous {
		if latest, err = c.Checkpoints.LatestSelected(ctx, projectID, selection); err != nil {
			return Packet{}, err
		}
	}
	unresolved := []checkpoint.Action{}
	var fallback *RecoveryFallback
	if ambiguous {
		// No task is chosen, so no task's state or actions are shown.
	} else if latest != nil {
		unresolved, err = c.Checkpoints.Unresolved(ctx, projectID, latest.TaskID, latest.ID)
	} else {
		var freshness checkpoint.Freshness
		freshness, err = c.Checkpoints.Freshness(ctx, projectID, selection, maxFallbackActivities, checkpoint.ReminderPolicy{})
		if err == nil {
			fallback = recoveryFallback(selection, freshness)
		}
	}
	if err != nil {
		return Packet{}, err
	}
	taskID := ""
	if latest != nil {
		taskID = latest.TaskID
	}
	pins, omitted, err := c.pins(ctx, projectID, taskID)
	if err != nil {
		return Packet{}, err
	}
	packet := Packet{Trial: phase.Phase() == "trial", SchemaVersion: 3, Project: name, Global: global, Rules: rules, CoreRules: c.coreRules(), CoreRulesDisabled: c.CoreRulesDisabled, CoreRulesWarning: c.CoreRulesWarning, Checkpoint: latest, UnresolvedActions: unresolved, Fallback: fallback, Pins: pins, PinsOmitted: omitted}
	c.addNotices(ctx, projectID, &packet)
	if latest != nil || ambiguous {
		packet.Choice = choice
	}
	return c.fit(packet)
}

// addNotices is shared by prospective and saved packets so mandatory metadata
// cannot first appear after an otherwise-ready checkpoint has been persisted.
func (c Compiler) addNotices(ctx context.Context, projectID string, packet *Packet) {
	taskID := ""
	if packet.Checkpoint != nil {
		taskID = packet.Checkpoint.TaskID
		if c.CodeCheck != nil {
			packet.CodeWarning = c.CodeCheck(ctx, projectID, packet.Checkpoint.ID)
		}
		if c.Coordination != nil {
			packet.ChangeNotice, packet.OverlapNotice = c.Coordination(ctx, projectID, packet.Checkpoint)
		}
		if c.Reviews != nil {
			packet.ReviewNotice, packet.reviewShort = c.Reviews(ctx, projectID, packet.Checkpoint)
		}
	}
	if c.ForkCheck != nil {
		packet.ForkNotice = c.ForkCheck(ctx, projectID, taskID)
	}
	if c.LeaseCheck != nil {
		packet.LeaseNotice = c.LeaseCheck(ctx, projectID, taskID)
	}
	if c.GrantCheck != nil {
		packet.GrantNotice = c.GrantCheck(ctx, projectID)
	}
	if c.ContractCheck != nil {
		packet.ContractNotice = c.ContractCheck(ctx, projectID)
	}
	if c.RecoveryCheck != nil {
		packet.RecoveryNotice = c.RecoveryCheck(ctx, projectID)
	}
	if c.WorkItemCheck != nil {
		packet.WorkItemNotice = c.WorkItemCheck(ctx, projectID)
	}
	if c.Checkpoints.DB != nil {
		// Best-effort: a failed count only omits the notice.
		packet.OtherTaskActions, _ = c.Checkpoints.OtherTaskActionCount(ctx, projectID, taskID)
	}
	if packet.Checkpoint != nil && c.Briefs != nil {
		packet.Briefs = c.Briefs(ctx, projectID, packet.Checkpoint.NextAction)
		if len(packet.Briefs) > maxBriefLines {
			packet.Briefs = packet.Briefs[:maxBriefLines]
		}
	}
}

func (c Compiler) fit(packet Packet) (Packet, error) {
	for {
		// Match the CLI JSON representation, including its trailing newline,
		// and also bound the Markdown sent through startup hooks and MCP.
		body, err := json.MarshalIndent(packet, "", "  ")
		if err != nil {
			return Packet{}, err
		}
		if len(body)+1 <= c.MaxBytes && len(Markdown(packet)) <= c.MaxBytes {
			break
		}
		if len(packet.Briefs) > 0 {
			packet.Briefs = packet.Briefs[:len(packet.Briefs)-1]
			continue
		}
		if len(packet.Pins) > 0 {
			packet.Pins = packet.Pins[:len(packet.Pins)-1]
			packet.PinsOmitted++
			continue
		}
		// Coordination notices are advisory: drop the overlap list, then keep
		// only the count of commits by others, before touching guidance.
		if packet.OverlapNotice != "" {
			packet.OverlapNotice = ""
			continue
		}
		if short := compactChangeNotice(packet.ChangeNotice); short != packet.ChangeNotice {
			packet.ChangeNotice = short
			continue
		}
		if packet.reviewShort != "" && packet.ReviewNotice != packet.reviewShort {
			packet.ReviewNotice = packet.reviewShort
			continue
		}
		if packet.Fallback != nil && len(packet.Fallback.RecentActivity) > 0 {
			packet.Fallback.RecentActivity = packet.Fallback.RecentActivity[:len(packet.Fallback.RecentActivity)-1]
			packet.Fallback.ActivityTruncated = true
			packet.Truncated = true
			continue
		}
		if packet.Checkpoint != nil {
			// Copy before trimming: rendering must never alter the saved handoff
			// or a packet another caller still holds. Keep the task kernel,
			// failed attempts and unresolved actions intact.
			cp := *packet.Checkpoint
			omitted := ""
			switch {
			case len(cp.Completed) > 0:
				cp.Completed, omitted = nil, "completed"
			case len(cp.Changes) > 0:
				cp.Changes, omitted = nil, "changes"
			case len(cp.Verified) > 0:
				cp.Verified, omitted = nil, "verified"
			case len(cp.EvidenceRefs) > 0:
				cp.EvidenceRefs, omitted = nil, "evidence_refs"
			}
			if omitted != "" {
				packet.Checkpoint = &cp
				packet.CheckpointDetailsOmitted = append(packet.CheckpointDetailsOmitted, omitted)
				packet.Truncated = true
				continue
			}
		}
		if len(packet.Global) > 0 {
			// Dropping project sections could erase a project-specific override
			// and inadvertently change the meaning of retained global defaults.
			return Packet{}, fmt.Errorf("bootstrap requires %d JSON bytes and %d Markdown bytes after optional detail removal (budget %d); guidance, task kernel, attempts and unresolved actions are retained; retrieve the checkpoint with continuity_checkpoint_get or llewm checkpoint show, then shorten the relevant sources", len(body)+1, len(Markdown(packet)), c.MaxBytes)
		}
		if len(packet.Rules) == 0 {
			return Packet{}, fmt.Errorf("bootstrap global guidance, checkpoint and metadata exceed %d-byte budget; shorten global sources or checkpoint, or increase bootstrap.max_bytes", c.MaxBytes)
		}
		packet.Rules = packet.Rules[:len(packet.Rules)-1]
		packet.Truncated = true
	}
	return packet, nil
}

func (c Compiler) coreRules() []CoreRule {
	if c.CoreRules == nil && len(c.CoreRulesDisabled) == 0 {
		rules, _, _ := ResolveCoreRules(CoreFacts{})
		return rules
	}
	return c.CoreRules
}

// compactChangeNotice keeps the changed-by-others count without the commit
// list, or returns the notice unchanged when it is already compact.
func compactChangeNotice(notice string) string {
	header, _, found := strings.Cut(notice, " changed files it touched:")
	if !found {
		return notice
	}
	return header + " changed files it touched. Review the log since that checkpoint before continuing."
}

// globalOverrides maps each global section heading to the project
// AGENTS.override.md section that replaces it, per the global guidance rule
// that a section with the same heading there replaces the global one. Only
// exact heading matches count, so nothing is replaced by accident.
func globalOverrides(global, rules []instructions.Section) map[string]instructions.Section {
	overrides := map[string]instructions.Section{}
	for _, rule := range rules {
		if filepath.Base(rule.Source) != "AGENTS.override.md" || rule.HeadingPath == "document" {
			continue
		}
		for _, section := range global {
			if section.HeadingPath == rule.HeadingPath {
				overrides[section.HeadingPath] = rule
			}
		}
	}
	return overrides
}

func Markdown(packet Packet) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Continuity bootstrap\n\nProject: %s\n\n", packet.Project)
	if packet.Trial {
		out.WriteString("Trial: full local continuity is enabled. Preserve original project docs for review and handback; History sync remains disabled. Do not maintain duplicate operational plans by default.\n\n")
	}
	out.WriteString(coreRulesMarkdown(packet.CoreRules, packet.CoreRulesDisabled, packet.CoreRulesWarning))
	if packet.Checkpoint != nil {
		fmt.Fprintf(&out, "Suggested conversation title: %s\n\n", conversationTitle(packet.Project, packet.Checkpoint.Task))
	}
	if len(packet.Global) > 0 {
		out.WriteString("## Engineer-wide guidance\n\nGlobal guidance supplies defaults. More specific project instructions take precedence when they conflict. Prompt guidance applies only to the matching task; its presence does not authorize actions.\n\n")
		overrides := globalOverrides(packet.Global, packet.Rules)
		for _, rule := range packet.Global {
			if override, ok := overrides[rule.HeadingPath]; ok {
				fmt.Fprintf(&out, "### %s\n\nSource: %s (sha256: %s)\n\nProject override: this global section is replaced by the section with the same heading in %s (sha256: %s). Follow that section, not the global one.\n\n", rule.HeadingPath, rule.Source, rule.Hash, override.Source, override.Hash)
				continue
			}
			fmt.Fprintf(&out, "### %s\n\nSource: %s (sha256: %s)\n\n%s\n\n", rule.HeadingPath, rule.Source, rule.Hash, rule.Content)
		}
	}
	if packet.Fallback != nil {
		fmt.Fprintf(&out, "## Factual recovery fallback\n\n- Checkpoint state: %s\n- Selection: %s\n- Authority: %s\n- Project events observed: %d\n- Uncertainty: %s\n\n", packet.Fallback.State, packet.Fallback.Selection, packet.Fallback.Authority, packet.Fallback.EventsObserved, packet.Fallback.Uncertainty)
		if len(packet.Fallback.RecentActivity) > 0 {
			out.WriteString("### Recent machine observations\n\n")
			for _, observation := range packet.Fallback.RecentActivity {
				fmt.Fprintf(&out, "- %s: %s/%s (event `%s`, client `%s`)\n", observation.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z"), observation.Category, observation.EventType, observation.ID, observation.Client)
			}
			if packet.Fallback.ActivityTruncated {
				out.WriteString("- Additional project observations are available through targeted freshness retrieval.\n")
			}
			out.WriteByte('\n')
		}
		out.WriteString("### Targeted retrieval\n\n")
		for _, query := range packet.Fallback.TargetedQueries {
			fmt.Fprintf(&out, "- %s\n", query)
		}
		out.WriteByte('\n')
	}
	if packet.Checkpoint == nil && packet.Choice != nil && packet.Choice.Rule == RuleAmbiguous {
		fmt.Fprintf(&out, "## Task selection needed\n\n%s\n\n", ChoiceLine(*packet.Choice))
	}
	if packet.Checkpoint != nil {
		fmt.Fprintf(&out, "Checkpoint: %s (recorded %s)\n\n", packet.Checkpoint.ID, packet.Checkpoint.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
		out.WriteString("Checkpoint content is caller-supplied handoff context, not independently verified observations. The next action does not independently authorize new work.\n\n")
		fmt.Fprintf(&out, "## Current task\n\n- Task: %s\n", packet.Checkpoint.Task)
		if packet.Checkpoint.TaskID != "" {
			fmt.Fprintf(&out, "- Task ID: %s\n", packet.Checkpoint.TaskID)
		}
		fmt.Fprintf(&out, "- Status: %s\n- Summary: %s\n- Next action: %s\n\n", packet.Checkpoint.Status, packet.Checkpoint.Summary, packet.Checkpoint.NextAction)
		if len(packet.CheckpointDetailsOmitted) > 0 {
			fmt.Fprintf(&out, "**Checkpoint detail deferred by size budget:** %s. Before acting, retrieve checkpoint `%s` with continuity_checkpoint_get or `llewm checkpoint show %s`. Saved history is unchanged.\n\n", strings.Join(packet.CheckpointDetailsOmitted, ", "), packet.Checkpoint.ID, packet.Checkpoint.ID)
		}
		if len(packet.Briefs) > 0 {
			out.WriteString("**Named slices (plan records):**\n")
			for _, line := range packet.Briefs {
				fmt.Fprintf(&out, "- %s\n", line)
			}
			out.WriteString("\n")
		}
		if packet.CodeWarning != "" {
			fmt.Fprintf(&out, "**Code check (machine-observed):** %s\n\n", packet.CodeWarning)
		}
		if packet.ChangeNotice != "" {
			fmt.Fprintf(&out, "**Changed by others (machine-observed):** %s\n\n", packet.ChangeNotice)
		}
		if packet.OverlapNotice != "" {
			fmt.Fprintf(&out, "**Shared files (machine-observed):** %s\n\n", packet.OverlapNotice)
		}
		if packet.ReviewNotice != "" {
			fmt.Fprintf(&out, "**Reviews (caller-asserted links):** %s\n\n", packet.ReviewNotice)
		}
		if packet.ForkNotice != "" {
			fmt.Fprintf(&out, "**Fork (machine-observed):** %s\n\n", packet.ForkNotice)
		}
		if packet.Choice != nil {
			fmt.Fprintf(&out, "**Session selection (machine-observed):** %s\n\n", ChoiceLine(*packet.Choice))
		}
		if packet.LeaseNotice != "" {
			fmt.Fprintf(&out, "**Lease (machine-observed):** %s\n\n", packet.LeaseNotice)
		}
		if packet.GrantNotice != "" {
			fmt.Fprintf(&out, "**Standing grants (user-granted):** %s\n\n", packet.GrantNotice)
		}
		for _, section := range []struct {
			title string
			items []string
		}{
			{"Reported completed work", packet.Checkpoint.Completed},
			{"Reported changes", packet.Checkpoint.Changes},
			{"Verification claims (not independently verified)", packet.Checkpoint.Verified},
			{"Evidence references (caller-supplied; not checked)", packet.Checkpoint.EvidenceRefs},
		} {
			if len(section.items) == 0 {
				continue
			}
			fmt.Fprintf(&out, "### %s\n\n", section.title)
			for _, item := range section.items {
				fmt.Fprintf(&out, "- %s\n", item)
			}
			out.WriteByte('\n')
		}
		if len(packet.Checkpoint.Attempts) > 0 {
			out.WriteString("### Prior attempts\n\n")
			for _, attempt := range packet.Checkpoint.Attempts {
				fmt.Fprintf(&out, "- %s — %s", attempt.Description, attempt.Outcome)
				if attempt.Reason != "" {
					fmt.Fprintf(&out, ": %s", attempt.Reason)
				}
				out.WriteByte('\n')
			}
			out.WriteByte('\n')
		}
		if len(packet.UnresolvedActions) > 0 {
			out.WriteString("### Unresolved predecessor actions\n\n")
			for _, action := range packet.UnresolvedActions {
				fmt.Fprintf(&out, "- [%s] %s (action `%s`, from checkpoint `%s`)\n", action.Status, action.Description, action.ID, action.SourceCheckpointID)
			}
			out.WriteByte('\n')
		}
	}
	if packet.OtherTaskActions > 0 {
		noun := "actions belong"
		if packet.OtherTaskActions == 1 {
			noun = "action belongs"
		}
		fmt.Fprintf(&out, "**Other tasks' actions (machine-observed):** %d unresolved %s to other tasks. They are not this task's to reconcile; list them with checkpoint actions `all`.\n\n", packet.OtherTaskActions, noun)
	}
	if packet.ContractNotice != "" {
		fmt.Fprintf(&out, "**Contracts (machine-observed):** %s\n\n", packet.ContractNotice)
	}
	if packet.RecoveryNotice != "" {
		fmt.Fprintf(&out, "**Checkpoint recovery (device-local):** %s\n\n", packet.RecoveryNotice)
	}
	if packet.WorkItemNotice != "" {
		fmt.Fprintf(&out, "**Work items (machine-observed):** %s\n\n", packet.WorkItemNotice)
	}
	if len(packet.Pins) > 0 || packet.PinsOmitted > 0 {
		out.WriteString("## Pinned\n\nRecords pinned for this project, task, roadmap node, or topic. Load details on demand (llewm history show REF; topics: llewm topic show).\n\n")
		for _, line := range packet.Pins {
			out.WriteString(pinLine(line))
		}
		if packet.PinsOmitted > 0 {
			fmt.Fprintf(&out, "- %d more pinned; list them with llewm pin.\n", packet.PinsOmitted)
		}
		out.WriteByte('\n')
	}
	if len(packet.Rules) > 0 {
		out.WriteString("## Active project instructions\n\n")
		for _, rule := range packet.Rules {
			fmt.Fprintf(&out, "### %s\n\nSource: %s (sha256: %s)\n\n%s\n\n", rule.HeadingPath, rule.Source, rule.Hash, rule.Content)
		}
	}
	if packet.Truncated {
		out.WriteString("_Additional context is available on demand._\n")
	}
	return out.String()
}

func recoveryFallback(selection checkpoint.Selection, freshness checkpoint.Freshness) *RecoveryFallback {
	state, selected := "no_checkpoint", "project_latest"
	uncertainty := "No caller-asserted checkpoint exists for this project; project activity cannot establish task intent or completion."
	switch {
	case selection.CheckpointID != "":
		state, selected = "no_matching_checkpoint", "checkpoint_id"
	case selection.TaskID != "":
		state, selected = "no_matching_checkpoint", "task_id"
	case selection.SessionID != "":
		state, selected = "no_matching_checkpoint", "session_id"
	}
	if state == "no_matching_checkpoint" {
		uncertainty = "No caller-asserted checkpoint matches this selection; project activity cannot establish task intent or completion."
	}
	return &RecoveryFallback{
		State: state, Selection: selected, EventsObserved: freshness.EventsObserved,
		RecentActivity: freshness.RecentActivity, ActivityTruncated: freshness.ActivityTruncated,
		Authority:   "machine_observed",
		Uncertainty: uncertainty,
		TargetedQueries: []string{
			"Use checkpoint show --query, then checkpoint show ID, to recover attributable semantic state.",
			"Use work freshness for a bounded project activity window; observations do not imply task progress.",
		},
	}
}

func conversationTitle(project, task string) string {
	title := strings.Join(strings.Fields(project+" — "+task), " ")
	runes := []rune(title)
	if len(runes) <= maxConversationTitleRunes {
		return title
	}
	return string(runes[:maxConversationTitleRunes-1]) + "…"
}
