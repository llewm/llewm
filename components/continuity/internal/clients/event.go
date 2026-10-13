package clients

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/redact"
)

type HookEnvelope struct {
	SessionID      string         `json:"session_id"`
	CWD            string         `json:"cwd"`
	TurnID         string         `json:"turn_id"`
	Prompt         string         `json:"prompt"`
	StopHookActive bool           `json:"stop_hook_active"`
	HookEvent      string         `json:"hook_event_name"`
	EventName      string         `json:"event_name"`
	Model          string         `json:"model"`
	ToolName       string         `json:"tool_name"`
	ToolInput      map[string]any `json:"tool_input"`
	ToolResult     any            `json:"tool_result"`
	ToolResponse   any            `json:"tool_response"`
	Error          string         `json:"error"`
	DurationMS     *int64         `json:"duration_ms"`
}

type Observation struct {
	Event      activity.Event
	Invocation attribution.Invocation
}

func DecodeObservation(client, projectID string, body []byte, storeRaw bool) (Observation, error) {
	event, err := Decode(client, projectID, body, storeRaw)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		Event: event,
		Invocation: attribution.Invocation{
			ProjectID: projectID, SessionID: event.SessionID, ActorType: "agent",
			Client: client, Model: event.ActorModel, IdentitySource: "client_hook_claim",
			OccurredAt: event.OccurredAt, ReceivedAt: event.ReceivedAt,
		},
	}, nil
}

func Decode(client, projectID string, body []byte, storeRaw bool) (activity.Event, error) {
	var envelope HookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return activity.Event{}, fmt.Errorf("decode %s hook: %w", client, err)
	}
	if strings.TrimSpace(string(body)) == "null" {
		return activity.Event{}, fmt.Errorf("decode %s hook: payload must be a JSON object", client)
	}
	name := envelope.HookEvent
	if name == "" {
		name = envelope.EventName
	}
	if name == "" {
		name = "unknown"
	}
	category := classify(name)
	event := activity.NewEvent(projectID, client, strings.ToLower(name), category)
	event.SessionID = envelope.SessionID
	event.DurationMS = envelope.DurationMS
	event.ActorType = "cloud_model"
	event.ActorModel = envelope.Model
	if envelope.Error != "" || strings.Contains(strings.ToLower(name), "failure") {
		event.Outcome = "failure"
	}
	if envelope.ToolName != "" {
		event.Payload = map[string]any{"tool": envelope.ToolName}
		if command, ok := envelope.ToolInput["command"].(string); ok {
			event.Payload["command"] = redact.String(command)
		}
		if path, ok := envelope.ToolInput["file_path"].(string); ok {
			event.Payload["file_path"] = path
		}
		if description, ok := envelope.ToolInput["description"].(string); ok {
			event.Payload["description"] = redact.String(description)
		}
		if envelope.Error != "" {
			event.Payload["error_signature"] = bounded(redact.String(strings.Join(strings.Fields(envelope.Error), " ")), 512)
		}
	}
	if storeRaw {
		var value any
		redacted := redact.String(string(body))
		if json.Unmarshal([]byte(redacted), &value) == nil {
			if event.Payload == nil {
				event.Payload = map[string]any{}
			}
			event.Payload["client_event"] = value
		}
	}
	return event, nil
}

func bounded(value string, maxBytes int) string {
	if len([]byte(value)) <= maxBytes {
		return value
	}
	body := []byte(value)[:maxBytes]
	for !utf8.Valid(body) {
		body = body[:len(body)-1]
	}
	return string(body)
}

func classify(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "tool"):
		return "tool"
	case strings.Contains(lower, "compact"):
		return "continuity"
	case strings.Contains(lower, "session"):
		return "session"
	case strings.Contains(lower, "prompt"):
		return "prompt"
	default:
		return "lifecycle"
	}
}
