package attribution

import "time"

const (
	SchemaVersion           = 1
	InvocationSchemaVersion = 2
)

type Effort struct {
	Raw        string `json:"raw"`
	Normalized string `json:"normalized,omitempty"`
	Source     string `json:"source"`
	Authority  string `json:"authority"`
}

type Invocation struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	SessionID          string    `json:"session_id,omitempty"`
	ParentInvocationID string    `json:"parent_invocation_id,omitempty"`
	ActorType          string    `json:"actor_type"`
	Client             string    `json:"client,omitempty"`
	ClientVersion      string    `json:"client_version,omitempty"`
	Provider           string    `json:"provider,omitempty"`
	Model              string    `json:"model,omitempty"`
	EffortRequested    *Effort   `json:"effort_requested,omitempty"`
	EffortEffective    *Effort   `json:"effort_effective,omitempty"`
	IdentitySource     string    `json:"identity_source"`
	OccurredAt         time.Time `json:"occurred_at"`
	ReceivedAt         time.Time `json:"received_at"`
	SchemaVersion      int       `json:"schema_version"`
}
