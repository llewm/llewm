package bootstrap

import (
	"strings"
	"testing"
)

func TestSubagentContextCarriesOnlyResolvedRulesWithinBudget(t *testing.T) {
	rules, disabled, err := ResolveCoreRules(CoreFacts{},
		CoreRuleLayer{Name: "project config", Config: CoreRuleConfig{Disable: []string{"own-commits"}, Override: map[string]string{"discovery": "Project wording."}}})
	if err != nil {
		t.Fatal(err)
	}
	got := Compiler{CoreRules: rules, CoreRulesDisabled: disabled}.SubagentContext("parent-session")
	for _, want := range []string{"# Continuity rules for this subagent", "parent session `parent-session`", "- discovery: Project wording. (overridden by project config)", "- task-identity: ", "- Disabled by configuration: own-commits."} {
		if !strings.Contains(got, want) {
			t.Fatalf("subagent context lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Omitted") || len(got) > SubagentMaxBytes {
		t.Fatalf("default rules should fit the budget (%d bytes):\n%s", len(got), got)
	}

	long := strings.Repeat("x", SubagentMaxBytes/2)
	rules, _, _ = ResolveCoreRules(CoreFacts{}, CoreRuleLayer{Name: "project config", Config: CoreRuleConfig{Override: map[string]string{"task-identity": long, "checkout-lifecycle": long}}})
	got = Compiler{CoreRules: rules}.SubagentContext("")
	if len(got) > SubagentMaxBytes || !strings.Contains(got, "- discovery: ") || !strings.Contains(got, "Omitted for the 4096-byte subagent budget: ") || !strings.Contains(got, "checkout-lifecycle") || strings.Contains(got, "for parent session") {
		t.Fatalf("oversized rules not trimmed from the end (%d bytes):\n%s", len(got), got)
	}
}
