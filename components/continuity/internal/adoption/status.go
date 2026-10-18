package adoption

// Status separates persisted configuration from client activation. A CLI read
// cannot establish which plugin or MCP process is loaded in a live session.
type Status struct {
	HookMode        string `json:"hook_mode"`
	AdoptionState   string `json:"adoption_state"`
	EffectiveHooks  string `json:"effective_hooks"`
	SessionRecovery string `json:"session_recovery"`
	LiveClient      string `json:"live_client"`
}

// Inspect reads only the receipt; no database, registry or client writes occur.
func Inspect(dir, mode string) (Status, error) {
	s, err := Load(dir)
	if err != nil {
		return Status{}, err
	}
	result := Status{HookMode: mode, AdoptionState: s.Phase(), EffectiveHooks: "full", SessionRecovery: "enabled", LiveClient: "not_verified"}
	if s == nil {
		result.AdoptionState = "not_recorded"
	}
	if mode == "audit-only" {
		result.EffectiveHooks, result.SessionRecovery = "passive_usage", "disabled_in_hooks"
	}
	if s.Phase() == "exited" {
		result.EffectiveHooks, result.SessionRecovery = "disabled", "disabled"
	}
	return result, nil
}
