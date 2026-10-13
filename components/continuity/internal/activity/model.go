package activity

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Event struct {
	ID            string         `json:"id"`
	OccurredAt    time.Time      `json:"occurred_at"`
	ReceivedAt    time.Time      `json:"received_at"`
	ProjectID     string         `json:"project_id"`
	SessionID     string         `json:"session_id,omitempty"`
	TaskID        string         `json:"task_id,omitempty"`
	Client        string         `json:"client"`
	EventType     string         `json:"event_type"`
	Category      string         `json:"category"`
	ActorType     string         `json:"actor_type"`
	ActorModel    string         `json:"actor_model,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	DurationMS    *int64         `json:"duration_ms,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	SchemaVersion int            `json:"schema_version"`
}

func NewID() string {
	random := make([]byte, 10)
	_, _ = rand.Read(random)
	return time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(random)
}

func NewEvent(projectID, client, eventType, category string) Event {
	now := time.Now().UTC()
	return Event{
		ID: NewID(), OccurredAt: now, ReceivedAt: now, ProjectID: projectID,
		Client: client, EventType: eventType, Category: category,
		ActorType: "unknown", SchemaVersion: 1,
	}
}
