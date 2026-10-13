package app

import (
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/bootstrap"
	"github.com/llewm/llewm/components/continuity/internal/config"
)

// resolveCoreRules applies the engineer-wide, then the project, [guidance.core]
// configuration to the built-in rules. Invalid configuration never fails the
// bootstrap: the built-in rules apply and the problem is reported in it.
func resolveCoreRules(globalRoot string, cfg config.Config) ([]bootstrap.CoreRule, []string, string) {
	clone, cloneErr := cfg.Sync.MemoryClone()
	facts := bootstrap.CoreFacts{MemoryClone: clone}
	builtIn, _, _ := bootstrap.ResolveCoreRules(facts)
	if cloneErr != nil {
		return builtIn, nil, cloneErr.Error()
	}
	global, err := config.LoadGlobalGuidance(globalRoot)
	if err != nil {
		return builtIn, nil, "reading " + filepath.Join(globalRoot, config.GlobalFileName) + ": " + err.Error()
	}
	rules, disabled, err := bootstrap.ResolveCoreRules(facts,
		bootstrap.CoreRuleLayer{Name: "global config", Config: bootstrap.CoreRuleConfig{Disable: global.Core.Disable, Override: global.Core.Override}},
		bootstrap.CoreRuleLayer{Name: "project config", Config: bootstrap.CoreRuleConfig{Disable: cfg.Guidance.Core.Disable, Override: cfg.Guidance.Core.Override}},
	)
	if err != nil {
		return builtIn, nil, err.Error()
	}
	return rules, disabled, ""
}
