package adoption

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Status separates persisted configuration from client activation. A CLI read
// cannot establish which plugin or MCP process is loaded in a live session.
type Status struct {
	Entry           string `json:"adoption_entry,omitempty"`
	ConfirmedAt     string `json:"adoption_confirmed_at,omitempty"`
	GuidanceNotice  string `json:"guidance_notice"`
	GuidanceState   string `json:"guidance_state"`
	HookMode        string `json:"hook_mode"`
	AdoptionState   string `json:"adoption_state"`
	EffectiveHooks  string `json:"effective_hooks"`
	SessionRecovery string `json:"session_recovery"`
	LiveClient      string `json:"live_client"`
}

// Inspect reads only the receipt; no database, registry or client writes occur.
func Inspect(root, dir, mode string) (Status, error) {
	s, err := Load(dir)
	if err != nil {
		return Status{}, err
	}
	result := Status{HookMode: mode, AdoptionState: s.Phase(), EffectiveHooks: "full", SessionRecovery: "enabled", LiveClient: "not_verified"}
	result.GuidanceState, result.GuidanceNotice = s.GuidanceStatus(root)
	if s.Phase() == "adopted" {
		for _, transition := range s.History {
			if transition.State == "adopted" {
				result.Entry = transition.Entry
				if result.Entry == "" {
					result.Entry = "trial"
				}
				result.ConfirmedAt = transition.At.UTC().Format(time.RFC3339)
				break
			}
		}
	}
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

func (s Status) DisplayState() string {
	if s.AdoptionState == "not_recorded" {
		return "no local adoption receipt (review with llewm adopt)"
	}
	if s.Entry != "" {
		return fmt.Sprintf("%s (%s, confirmed %s)", s.AdoptionState, s.Entry, s.ConfirmedAt)
	}
	return s.AdoptionState
}

// GuidanceStatus reports drift without treating an old workflow as current instructions.
func (s *State) GuidanceStatus(root string) (string, string) {
	if s.Phase() != "adopted" {
		return "unreviewed", "no recorded guidance review"
	}
	g := s.History[len(s.History)-1].Guidance
	if g == nil {
		return "unreviewed", "adopted; record the project workflow with llewm adopt --guidance and --workflow"
	}
	var paths []string
	for _, f := range g.Sources {
		paths = append(paths, f.Path)
	}
	current, err := Snapshot(root, paths, false)
	sources := strings.Join(paths, ", ")
	if err != nil || !reflect.DeepEqual(current, g.Sources) {
		return "changed", "review changes to " + sources + " before relying on the saved workflow"
	}
	return "reviewed", g.Workflow + " (reviewed sources: " + sources + ")"
}
