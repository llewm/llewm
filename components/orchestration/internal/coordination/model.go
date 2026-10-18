// Package coordination implements the team's publication authority. It does not
// control processes, provider accounts, source repositories or Continuity stores.
package coordination

import "time"

const SchemaVersion = 1

type Member struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
}

func (m Member) Can(permission string) bool {
	for _, p := range m.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

type Definition struct {
	Title     string `json:"title"`
	SourceRef string `json:"source_ref,omitempty"`
	Context   string `json:"context"`
	Items     []Item `json:"items"`
}

type Item struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Detail              string   `json:"detail"`
	AcceptanceCriteria  string   `json:"acceptance_criteria"`
	DependsOn           []string `json:"depends_on"`
	ContextRefs         []string `json:"context_refs,omitempty"`
	PriorCompletionRefs []string `json:"prior_completion_refs,omitempty"`
	BlockedReason       string   `json:"blocked_reason,omitempty"`
}

type Revision struct {
	Number     int        `json:"number"`
	Author     string     `json:"author"`
	At         time.Time  `json:"at"`
	Definition Definition `json:"definition"`
}

type Acceptance struct {
	Revision int       `json:"revision"`
	Reviewer string    `json:"reviewer"`
	At       time.Time `json:"at"`
}

type Assignment struct {
	Epoch      int       `json:"epoch"`
	Engineer   string    `json:"engineer"` // empty means available to eligible members
	AssignedBy string    `json:"assigned_by"`
	Reason     string    `json:"reason"`
	At         time.Time `json:"at"`
}

type Work struct {
	Assignment    *Assignment `json:"assignment,omitempty"` // nil inherits plan owner
	Generation    int         `json:"generation"`
	ActiveAttempt string      `json:"active_attempt,omitempty"`
	CompletedBy   string      `json:"completed_by,omitempty"` // submission ID or source:item-ID (see item's prior_completion_refs)
}

type Attempt struct {
	ID              string    `json:"id"`
	ItemID          string    `json:"item_id"`
	Engineer        string    `json:"engineer"`
	Machine         string    `json:"machine"`
	Agent           string    `json:"agent"`
	Revision        int       `json:"revision"`
	AssignmentEpoch int       `json:"assignment_epoch"`
	Inherited       bool      `json:"inherited"`
	Generation      int       `json:"generation"`
	StartedAt       time.Time `json:"started_at"`
	ValidUntil      time.Time `json:"valid_until"`
	Released        bool      `json:"released"`
}

type Usage struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Confidence   string `json:"confidence"` // reported or estimated; absence is unknown
}

type Contribution struct {
	ID               string    `json:"id"`
	AttemptID        string    `json:"attempt_id"`
	Engineer         string    `json:"engineer"`
	Kind             string    `json:"kind"` // checkpoint or result
	Context          string    `json:"context"`
	ArtifactRefs     []string  `json:"artifact_refs"`
	VerificationRefs []string  `json:"verification_refs"`
	Usage            *Usage    `json:"usage,omitempty"`
	ReceivedAt       time.Time `json:"received_at"`
	StaleAtReceipt   string    `json:"stale_at_receipt,omitempty"`
}

type Review struct {
	SubmissionID     string    `json:"submission_id"`
	Decision         string    `json:"decision"` // accept, adopt (explicit stale reconciliation), reject
	Reviewer         string    `json:"reviewer"`
	Reason           string    `json:"reason"`
	VerificationRefs []string  `json:"verification_refs"`
	At               time.Time `json:"at"`
}

type Plan struct {
	ID               string              `json:"id"`
	Version          int                 `json:"version"` // CAS for every authoritative plan mutation
	Revisions        []Revision          `json:"revisions"`
	Acceptances      []Acceptance        `json:"acceptances"`
	AcceptedRevision int                 `json:"accepted_revision"`
	Assignment       Assignment          `json:"assignment"`
	Work             map[string]*Work    `json:"work"`
	Attempts         map[string]*Attempt `json:"attempts"`
	Contributions    []Contribution      `json:"contributions"`
	Reviews          []Review            `json:"reviews"`
}

// Command is the versioned manual coordination interface. Actor identity comes
// only from authentication. An idempotency key must never name different input.
type Command struct {
	SchemaVersion    int         `json:"schema_version"`
	RequestID        string      `json:"request_id"`
	Action           string      `json:"action"`
	PlanID           string      `json:"plan_id"`
	BaseVersion      int         `json:"base_version,omitempty"`
	Definition       *Definition `json:"definition,omitempty"`
	Revision         int         `json:"revision,omitempty"`
	ItemID           string      `json:"item_id,omitempty"`
	Engineer         string      `json:"engineer,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	Machine          string      `json:"machine,omitempty"`
	Agent            string      `json:"agent,omitempty"`
	AttemptID        string      `json:"attempt_id,omitempty"`
	Kind             string      `json:"kind,omitempty"`
	Context          string      `json:"context,omitempty"`
	ArtifactRefs     []string    `json:"artifact_refs,omitempty"`
	VerificationRefs []string    `json:"verification_refs,omitempty"`
	Usage            *Usage      `json:"usage,omitempty"`
	SubmissionID     string      `json:"submission_id,omitempty"`
	Decision         string      `json:"decision,omitempty"`
}

type Receipt struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Actor         string `json:"actor"`
	PlanID        string `json:"plan_id"`
	Version       int    `json:"version"`
	AttemptID     string `json:"attempt_id,omitempty"`
	SubmissionID  string `json:"submission_id,omitempty"`
	Status        string `json:"status"`
	Notice        string `json:"notice,omitempty"`
}

type AttemptStatus struct {
	AttemptID    string `json:"attempt_id"`
	CanResume    bool   `json:"can_resume"`
	Reason       string `json:"reason,omitempty"`
	CurrentOwner string `json:"current_owner"`
}

type View struct {
	SchemaVersion int                `json:"schema_version"`
	Plan          *Plan              `json:"plan"`
	Ready         []string           `json:"ready"`
	Attempts      []AttemptStatus    `json:"attempts"`
	Submissions   []SubmissionStatus `json:"submissions"`
}

type SubmissionStatus struct {
	ID            string `json:"id"`
	PendingReview bool   `json:"pending_review"`
	StaleReason   string `json:"stale_reason,omitempty"`
	Decision      string `json:"decision,omitempty"`
}

type Event struct {
	Sequence   int64   `json:"sequence"`
	Actor      string  `json:"actor"`
	Command    Command `json:"command"`
	Receipt    Receipt `json:"receipt"`
	RecordedAt string  `json:"recorded_at"`
}

type History struct {
	SchemaVersion int     `json:"schema_version"`
	Events        []Event `json:"events"`
	NextAfter     int64   `json:"next_after"`
}
