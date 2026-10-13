package procedures

import (
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

// Procedure kinds. A check describes the work behind completion-profile
// source-check stages; a runbook records reviewed steps to take in a situation.
// Both are inert data: Continuity never runs a step.
const (
	KindCheck   = "check"
	KindRunbook = "runbook"
)

// NormalizeKind defaults an empty kind to check and accepts the former name
// completion_check as an alias for it.
func NormalizeKind(kind string) (string, error) {
	switch strings.TrimSpace(kind) {
	case "", KindCheck, "completion_check":
		return KindCheck, nil
	case KindRunbook:
		return KindRunbook, nil
	}
	return "", fmt.Errorf("unsupported procedure kind %q (use check or runbook)", kind)
}

type Candidate struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	NormalizedSteps  []string `json:"normalized_steps"`
	ObservationCount int      `json:"observation_count"`
	SuccessCount     int      `json:"success_count"`
	Score            float64  `json:"score"`
	Status           string   `json:"status"`
}

type Parameter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	// ResourceKind, when set, says the value names a resource of that kind
	// (host, repository, or service). Default is an optional resource ID.
	ResourceKind string `json:"resource_kind,omitempty"`
	Default      string `json:"default,omitempty"`
}

type Step struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	StageKind    string   `json:"stage_kind,omitempty"`
	StageID      string   `json:"stage_id,omitempty"`
	ArgvTemplate []string `json:"argv_template,omitempty"`
}

type Procedure struct {
	ProjectID        string      `json:"project_id"`
	ID               string      `json:"id"`
	Revision         int         `json:"revision"`
	Title            string      `json:"title"`
	Purpose          string      `json:"purpose"`
	Kind             string      `json:"kind"`
	Status           string      `json:"status"`
	Platforms        []string    `json:"platforms"`
	WorkingDirectory string      `json:"working_directory"`
	Parameters       []Parameter `json:"parameters"`
	DeclaredEffects  []string    `json:"declared_effects"`
	Validations      []string    `json:"validations"`
	PostRunChecks    []string    `json:"post_run_checks"`
	Steps            []Step      `json:"steps"`
	Source           string      `json:"source"`
	SourceRef        string      `json:"source_ref,omitempty"`
	Authority        string      `json:"authority"`
	RecordedAt       time.Time   `json:"recorded_at"`
	SchemaVersion    int         `json:"schema_version"`
}

// NormalizeCommand provides a deliberately conservative initial fingerprint.
// Shell-AST normalization and parameter abstraction belong in the next slice.
func NormalizeCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}
