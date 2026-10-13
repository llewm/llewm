package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
)

// Session selection rules, in precedence order.
const (
	RuleExplicit = "explicit"
	RulePresence = "presence"
	RuleSession  = "session"
	RuleBranch   = "branch"
	RuleNewest   = "newest"
	// RuleAmbiguous chooses nothing: several tasks are in progress and no
	// explicit selection, presence, or this session's own checkpoint picks one.
	RuleAmbiguous = "ambiguous"
)

// ContentionWindow bounds how recently an in-progress task must have been
// checkpointed to count as possibly being worked on by another agent.
const ContentionWindow = 24 * time.Hour

// maxOtherTasks bounds the other active tasks the bootstrap names.
const maxOtherTasks = 3

// Choice is how the bootstrap chose its task, and which other tasks are active.
// The branch is a hint, never a key: it only orders which task resumes.
type Choice struct {
	Selection      checkpoint.Selection `json:"-"`
	Rule           string               `json:"rule"`
	Detail         string               `json:"detail,omitempty"`
	RecentPresence string               `json:"recent_presence,omitempty"`
	Others         []OtherTask          `json:"other_tasks,omitempty"`
	// OthersOmitted counts active tasks beyond those named.
	OthersOmitted int `json:"other_tasks_omitted,omitempty"`
}

// OtherTask is an active task the bootstrap did not choose.
type OtherTask struct {
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	Branch    string    `json:"branch,omitempty"`
	Detached  bool      `json:"detached,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ChooseContext is what Choose knows about the session beyond tips and presence.
type ChooseContext struct {
	// SessionTask is the task this session last checkpointed, if any.
	SessionTask string
	Now         time.Time
	// Closed reports whether a task's lineage is closed; nil means none are.
	Closed func(taskID string) bool
	// PresentByOthers names an unexpired observation from another session.
	// It is a routing hint, never proof that a writer still exists.
	PresentByOthers func(taskID string) string
}

// Choose picks a task when nothing was selected explicitly: the first live-presence
// task (in the caller's order) that has a checkpoint; else the task this
// session last checkpointed; else the newest task whose latest checkpoint was
// written on a current branch; else the project-wide newest checkpoint. The
// branch and newest rules prefer unfinished tasks over completed ones. When
// the branch or newest rule would have to pick among several in-progress tasks
// checkpointed within ContentionWindow, it picks none (RuleAmbiguous) and names
// them, so a fresh session never silently takes over another agent's task.
// Tips are newest first.
func Choose(tips []checkpoint.TaskTip, present []string, current []checkpoint.CodeBranch, context ChooseContext) (choice Choice) {
	defer func() {
		if context.PresentByOthers != nil && choice.Selection.TaskID != "" {
			choice.RecentPresence = context.PresentByOthers(choice.Selection.TaskID)
		}
	}()
	unfinished := func(tip checkpoint.TaskTip) bool {
		return tip.Status != "completed" && (context.Closed == nil || !context.Closed(tip.TaskID))
	}
	tipByTask := map[string]checkpoint.TaskTip{}
	for _, tip := range tips {
		tipByTask[tip.TaskID] = tip
	}
	for _, taskID := range present {
		if tip, ok := tipByTask[taskID]; ok && unfinished(tip) {
			return Choice{Selection: checkpoint.Selection{TaskID: taskID}, Rule: RulePresence, Detail: "task:" + taskID + " has live presence from this session"}
		}
	}
	if tip, ok := tipByTask[context.SessionTask]; context.SessionTask != "" && ok && unfinished(tip) {
		return Choice{Selection: checkpoint.Selection{TaskID: context.SessionTask}, Rule: RuleSession, Detail: "task:" + context.SessionTask + " was last checkpointed by this session"}
	}
	contending := func(match func(checkpoint.TaskTip) bool) []checkpoint.TaskTip {
		var found []checkpoint.TaskTip
		for _, tip := range tips {
			if !match(tip) || tip.Status != "in_progress" || context.Now.Sub(tip.CreatedAt) > ContentionWindow || (context.Closed != nil && context.Closed(tip.TaskID)) {
				continue
			}
			found = append(found, tip)
		}
		return found
	}
	var names []string
	for _, branch := range current {
		if branch.Branch != "" {
			names = append(names, branchLabel(branch))
		}
	}
	onCurrent := func(tip checkpoint.TaskTip) (checkpoint.CodeBranch, bool) {
		for _, branch := range current {
			if branch.Branch != "" && tip.OnBranch(branch.Member, branch.Branch) {
				return branch, true
			}
		}
		return checkpoint.CodeBranch{}, false
	}
	// Prefer a task without another session's recent observation when one exists.
	// If all unfinished tasks have observations, keep the normal selection rules:
	// a missing Stop/SessionEnd can leave an abandoned session unexpired for its
	// full TTL. Only genuine task ambiguity warrants asking which to continue.
	heldBy := func(tip checkpoint.TaskTip) string {
		if context.PresentByOthers == nil {
			return ""
		}
		return context.PresentByOthers(tip.TaskID)
	}
	var free []checkpoint.TaskTip
	var held []checkpoint.TaskTip
	for _, tip := range tips {
		if heldBy(tip) == "" {
			free = append(free, tip)
		} else if unfinished(tip) {
			held = append(held, tip)
		}
	}
	if len(held) > 0 {
		hasFreeUnfinished := false
		for _, tip := range free {
			if unfinished(tip) {
				hasFreeUnfinished = true
				break
			}
		}
		if hasFreeUnfinished {
			tips = free
		}
	}
	for _, tip := range tips {
		branch, ok := onCurrent(tip)
		if !ok || !unfinished(tip) {
			continue
		}
		if rivals := contending(func(t checkpoint.TaskTip) bool { _, on := onCurrent(t); return on }); len(rivals) > 1 {
			return ambiguous(rivals, "on the current branch "+branchLabel(branch))
		}
		return Choice{Selection: checkpoint.Selection{TaskID: tip.TaskID}, Rule: RuleBranch, Detail: "its latest checkpoint was on the current branch " + branchLabel(branch)}
	}
	if rivals := contending(func(checkpoint.TaskTip) bool { return true }); len(rivals) > 1 {
		return ambiguous(rivals, "in this project")
	}
	detail := "no presence or current-branch match"
	if len(names) > 0 {
		detail = "no task was last checkpointed on " + strings.Join(names, ", ")
	}
	for _, tip := range tips {
		if unfinished(tip) {
			return Choice{Selection: checkpoint.Selection{TaskID: tip.TaskID}, Rule: RuleNewest, Detail: detail + "; newest unfinished task"}
		}
	}
	return Choice{Rule: RuleNewest, Detail: detail}
}

// ambiguous names the contending tasks, bounded like OtherActive.
func ambiguous(rivals []checkpoint.TaskTip, where string) Choice {
	choice := Choice{Rule: RuleAmbiguous, Detail: fmt.Sprintf("%d tasks are in progress %s, each checkpointed within %s, and no presence or checkpoint of this session selects one", len(rivals), where, ContentionWindow)}
	choice.Others, choice.OthersOmitted = OtherActive(rivals, "", nil)
	return choice
}

// OtherActive lists the tasks other than chosen whose latest checkpoint is not
// completed and whose lineage is open, newest first and bounded.
func OtherActive(tips []checkpoint.TaskTip, chosen string, closed func(taskID string) bool) ([]OtherTask, int) {
	var others []OtherTask
	omitted := 0
	for _, tip := range tips {
		if tip.TaskID == chosen || tip.Status == "completed" || (closed != nil && closed(tip.TaskID)) {
			continue
		}
		if len(others) == maxOtherTasks {
			omitted++
			continue
		}
		other := OtherTask{TaskID: tip.TaskID, Status: tip.Status, CreatedAt: tip.CreatedAt}
		if len(tip.Branches) > 0 {
			// Branches are in member order, so the primary repository comes first.
			other.Branch, other.Detached = tip.Branches[0].Branch, tip.Branches[0].Branch == ""
			if tip.Branches[0].Member != "" && other.Branch != "" {
				other.Branch = tip.Branches[0].Member + "@" + other.Branch
			}
		}
		others = append(others, other)
	}
	return others, omitted
}

func branchLabel(branch checkpoint.CodeBranch) string {
	if branch.Member == "" {
		return "`" + branch.Branch + "`"
	}
	return "`" + branch.Member + "@" + branch.Branch + "`"
}

// ChoiceLine renders a choice as one bootstrap line.
func ChoiceLine(choice Choice) string {
	var out strings.Builder
	switch choice.Rule {
	case RuleExplicit:
		out.WriteString("Chosen by explicit selection.")
	case RulePresence:
		fmt.Fprintf(&out, "Chosen by presence: %s.", choice.Detail)
	case RuleSession:
		fmt.Fprintf(&out, "Chosen by session: %s.", choice.Detail)
	case RuleBranch:
		fmt.Fprintf(&out, "Chosen by branch: %s.", choice.Detail)
	case RuleAmbiguous:
		fmt.Fprintf(&out, "No task chosen: %s. Another agent may be working on any of them. Ask the user which task to continue, then fetch the bootstrap with that task_id; do not checkpoint or reconcile actions until a task is selected.", choice.Detail)
	default:
		fmt.Fprintf(&out, "Chosen as the project's newest checkpoint (%s).", choice.Detail)
	}
	out.WriteString(" Order: explicit, presence, session, current branch, newest.")
	if choice.RecentPresence != "" {
		fmt.Fprintf(&out, " Recent presence observation: %s. This expiring record does not prove a current writer; check Git state and confirm concurrency before creating a worktree.", choice.RecentPresence)
	}
	if len(choice.Others) > 0 {
		parts := make([]string, 0, len(choice.Others))
		for _, other := range choice.Others {
			where := "branch unknown"
			switch {
			case other.Detached:
				where = "detached HEAD"
			case other.Branch != "":
				where = "on `" + other.Branch + "`"
			}
			parts = append(parts, fmt.Sprintf("`%s` (%s, %s)", other.TaskID, other.Status, where))
		}
		fmt.Fprintf(&out, " Other active tasks: %s", strings.Join(parts, ", "))
		if choice.OthersOmitted > 0 {
			fmt.Fprintf(&out, " and %d more", choice.OthersOmitted)
		}
		out.WriteString("; select one with --task-id or task_id.")
	}
	return out.String()
}
