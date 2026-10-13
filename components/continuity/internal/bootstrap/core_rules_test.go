package bootstrap

import (
	"strings"
	"testing"
)

func TestResolveCoreRulesAppliesLayersAndRejectsUnknownIDs(t *testing.T) {
	rules, disabled, err := ResolveCoreRules(CoreFacts{})
	if err != nil || len(rules) != len(DefaultCoreRules)-1 || len(disabled) != 0 {
		t.Fatalf("defaults: %#v %v %v", rules, disabled, err)
	}
	rules, disabled, err = ResolveCoreRules(CoreFacts{},
		CoreRuleLayer{Name: "global config", Config: CoreRuleConfig{Override: map[string]string{"discovery": "Global wording."}}},
		CoreRuleLayer{Name: "project config", Config: CoreRuleConfig{Disable: []string{"own-commits"}, Override: map[string]string{"discovery": "Project wording."}}},
	)
	if err != nil || len(rules) != 2 || rules[0].Text != "Project wording." || rules[0].Source != "overridden by project config" || len(disabled) != 1 || disabled[0] != "own-commits" {
		t.Fatalf("layers: %#v %v %v", rules, disabled, err)
	}
	got := coreRulesMarkdown(rules, disabled, "")
	for _, want := range []string{"## Continuity rules (built in)", "- discovery: Project wording. (overridden by project config)", "- task-identity: ", "- Disabled by configuration: own-commits."} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, got)
		}
	}
	for name, layer := range map[string]CoreRuleConfig{
		"unknown disable":  {Disable: []string{"discvery"}},
		"unknown override": {Override: map[string]string{"nope": "x"}},
		"empty override":   {Override: map[string]string{"discovery": " "}},
	} {
		if _, _, err := ResolveCoreRules(CoreFacts{}, CoreRuleLayer{Name: "project config", Config: layer}); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestMemoryRepositoryRuleNeedsSyncAndNamesTheClone(t *testing.T) {
	unsynced, _, _ := ResolveCoreRules(CoreFacts{})
	for _, rule := range unsynced {
		if rule.ID == "memory-repository" {
			t.Fatal("memory-repository rule shown without sync")
		}
	}
	synced, _, err := ResolveCoreRules(CoreFacts{MemoryClone: "/data/memory"})
	if err != nil || len(synced) != len(DefaultCoreRules) {
		t.Fatalf("synced rules: %#v %v", synced, err)
	}
	last := synced[len(synced)-1]
	if last.ID != "memory-repository" || !strings.Contains(last.Text, "clone at `/data/memory` is managed by Continuity sync") || strings.Contains(last.Text, "{clone}") {
		t.Fatalf("memory rule: %#v", last)
	}
	// Overrides may use the placeholder too; disabling works like any rule.
	overridden, _, _ := ResolveCoreRules(CoreFacts{MemoryClone: "/data/memory"}, CoreRuleLayer{Name: "project config", Config: CoreRuleConfig{Override: map[string]string{"memory-repository": "Leave {clone} alone."}}})
	if got := overridden[len(overridden)-1].Text; got != "Leave `/data/memory` alone." {
		t.Fatalf("override placeholder: %q", got)
	}
	if _, disabled, _ := ResolveCoreRules(CoreFacts{MemoryClone: "/data/memory"}, CoreRuleLayer{Name: "project config", Config: CoreRuleConfig{Disable: []string{"memory-repository"}}}); len(disabled) != 1 {
		t.Fatalf("memory rule not disabled: %v", disabled)
	}
}
