package bootstrap

import (
	"strconv"
	"strings"
)

// SubagentMaxBytes bounds the subagent packet. Clients do not pass the parent
// session's startup context to subagents, so this packet carries only the
// resolved Continuity rules: no checkpoint, guidance sections or history.
const SubagentMaxBytes = 4096

// SubagentContext returns the resolved Continuity rules for a subagent of
// parentSessionID, within SubagentMaxBytes. Rules that do not fit are dropped
// from the end and the packet says so.
func (c Compiler) SubagentContext(parentSessionID string) string {
	rules := c.coreRules()
	header := "# Continuity rules for this subagent\n\nSource: Llewm built-in rules after global and project configuration, delivered at SubagentStart"
	if parentSessionID != "" {
		header += " for parent session `" + parentSessionID + "`"
	}
	header += ". The parent session owns its task's checkpoints.\n\n"
	for kept := len(rules); kept >= 0; kept-- {
		body := coreRulesMarkdown(rules[:kept], c.CoreRulesDisabled, c.CoreRulesWarning)
		if kept < len(rules) {
			body = strings.TrimSuffix(body, "\n") + "- Omitted for the " + strconv.Itoa(SubagentMaxBytes) + "-byte subagent budget: " + ruleIDs(rules[kept:]) + ". Read them with continuity_bootstrap.\n\n"
		}
		if out := header + body; len(out) <= SubagentMaxBytes {
			return out
		}
	}
	return header + "- All rules omitted for the subagent budget. Read them with continuity_bootstrap.\n"
}

func ruleIDs(rules []CoreRule) string {
	ids := make([]string, len(rules))
	for i, rule := range rules {
		ids[i] = rule.ID
	}
	return strings.Join(ids, ", ")
}
