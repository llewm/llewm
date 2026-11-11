package bootstrap

import (
	"fmt"
	"strings"
)

// CoreRule is a Continuity rule whose correctness depends on how Continuity
// works, so the binary owns its text and states it at the top of every
// bootstrap. Configuration can disable or reword a rule, never silently.
type CoreRule struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Source is "built-in", or the configuration that overrides the text.
	Source string `json:"source"`
	// requires names the project fact a conditional rule needs; "" means
	// always. "sync" needs a configured memory repository.
	requires string
}

// CoreFacts are the project facts conditional rules depend on.
type CoreFacts struct {
	// MemoryClone is the memory repository clone path when sync is configured.
	MemoryClone string
}

// DefaultCoreRules are the built-in rules, in bootstrap order.
var DefaultCoreRules = []CoreRule{
	{ID: "discovery", Source: "built-in", Text: "Before diagnosing or redesigning existing behavior, search history with focused terms (continuity_history_search) and expand only relevant refs. Reuse loaded evidence; no useful match means go to code. History is prior intent, not proof or authority; verify it against current code. Skip for mechanical edits."},
	{ID: "task-identity", Source: "built-in", Text: "Checkpoint under your own task_id from its current tip. To review or repair another agent's task, use a new task_id with reviews_task_id; never write to another agent's task or actions."},
	{ID: "own-commits", Source: "built-in", Text: "Narrate only commits your session or task made; another task's commits belong to its agent."},
	{ID: "resume-completion", Source: "built-in", Text: "On resumption, especially after a client restart, check whether the selected task's remaining action is already done: verify runtime and exact checkpoint recovery. If all acceptance checks pass, reconcile its actions and save completed this turn. A restart alone is not completion. Keep unrelated unfinished work explicit; review other tasks only when explicitly selected or authorized for lifecycle reconciliation."},
	{ID: "checkout-lifecycle", Source: "built-in", Text: "Default new and resumed sequential single-session tasks to the current checkout and branch. Create a task branch/worktree only for confirmed concurrent writers, explicit isolation, or a required project workflow; open tasks, leases, branches and expiring presence alone do not prove concurrency. If unsure, check continuity_presence and Git state; respect the user's single-session report. After integration, remove the task worktree AND local branch only if clean, with no unique commits or live writer; otherwise report why kept."},
	{ID: "memory-repository", Source: "built-in", requires: "sync", Text: "Continuity sync manages the memory repository clone at {clone}. Never hand-edit, commit to, rebase, reset, or force-push it or its remote; its automatic `continuity: <instance> +N records` commits need no Git note or work narrative."},
}

// CoreRuleConfig disables or rewords built-in rules by ID.
type CoreRuleConfig struct {
	Disable  []string
	Override map[string]string
}

// CoreRuleLayer is one configuration source, applied in order.
type CoreRuleLayer struct {
	Name   string
	Config CoreRuleConfig
}

// ResolveCoreRules applies the global then the project configuration to the
// built-in rules. It returns the active rules and the IDs disabled, and refuses
// unknown IDs so a typo cannot silently leave a rule in force or disabled.
func ResolveCoreRules(facts CoreFacts, layers ...CoreRuleLayer) ([]CoreRule, []string, error) {
	known := map[string]bool{}
	for _, rule := range DefaultCoreRules {
		known[rule.ID] = true
	}
	disabled := map[string]bool{}
	override := map[string]CoreRule{}
	for _, layer := range layers {
		for _, id := range layer.Config.Disable {
			if !known[id] {
				return nil, nil, fmt.Errorf("%s: unknown core rule %q in [guidance.core] disable", layer.Name, id)
			}
			disabled[id] = true
		}
		for id, text := range layer.Config.Override {
			if !known[id] {
				return nil, nil, fmt.Errorf("%s: unknown core rule %q in [guidance.core.override]", layer.Name, id)
			}
			if text = strings.TrimSpace(text); text == "" {
				return nil, nil, fmt.Errorf("%s: empty override for core rule %q; disable it instead", layer.Name, id)
			}
			override[id] = CoreRule{ID: id, Text: text, Source: "overridden by " + layer.Name}
		}
	}
	var active []CoreRule
	var off []string
	for _, rule := range DefaultCoreRules {
		if rule.requires == "sync" && facts.MemoryClone == "" {
			continue
		}
		if disabled[rule.ID] {
			off = append(off, rule.ID)
			continue
		}
		if replaced, ok := override[rule.ID]; ok {
			rule = replaced
		}
		rule.Text = strings.ReplaceAll(rule.Text, "{clone}", "`"+facts.MemoryClone+"`")
		active = append(active, rule)
	}
	return active, off, nil
}

func coreRulesMarkdown(rules []CoreRule, disabled []string, warning string) string {
	if len(rules) == 0 && len(disabled) == 0 && warning == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString("## Continuity rules (built in)\n\n")
	for _, rule := range rules {
		fmt.Fprintf(&out, "- %s: %s", rule.ID, rule.Text)
		if rule.Source != "built-in" {
			fmt.Fprintf(&out, " (%s)", rule.Source)
		}
		out.WriteByte('\n')
	}
	if len(disabled) > 0 {
		fmt.Fprintf(&out, "- Disabled by configuration: %s.\n", strings.Join(disabled, ", "))
	}
	if warning != "" {
		fmt.Fprintf(&out, "- Configuration ignored: %s. The built-in rules apply.\n", warning)
	}
	out.WriteByte('\n')
	return out.String()
}
