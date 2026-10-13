package clients

import (
	"strings"
	"testing"
)

func TestDecodeRejectsNonObject(t *testing.T) {
	for _, body := range []string{"null", "[]", `"text"`, "{"} {
		if _, err := Decode("codex", "p", []byte(body), false); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestDecodeToolEvent(t *testing.T) {
	body := []byte(`{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Bash","duration_ms":12}`)
	event, err := Decode("claude-code", "p", body, false)
	if err != nil {
		t.Fatal(err)
	}
	if event.Category != "tool" || event.SessionID != "s" || event.DurationMS == nil || *event.DurationMS != 12 {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestDecodeObservationPreservesClaimedIdentity(t *testing.T) {
	body := []byte(`{"session_id":"s","hook_event_name":"SessionStart","model":"model-a"}`)
	observation, err := DecodeObservation("codex", "p", body, false)
	if err != nil {
		t.Fatal(err)
	}
	invocation := observation.Invocation
	if invocation.ProjectID != "p" || invocation.SessionID != "s" || invocation.Client != "codex" || invocation.Model != "model-a" {
		t.Fatalf("unexpected invocation: %#v", invocation)
	}
	if invocation.ActorType != "agent" || invocation.IdentitySource != "client_hook_claim" {
		t.Fatalf("hook claim was given misleading provenance: %#v", invocation)
	}
}

func TestDecodeFailureRetainsOnlyBoundedRedactedSignature(t *testing.T) {
	body := []byte(`{"session_id":"s","hook_event_name":"PostToolUseFailure","tool_name":"Bash","tool_input":{"command":"go test ./..."},"error":"permission denied token=fixture-client-secret ` + strings.Repeat("x", 700) + `"}`)
	event, err := Decode("claude-code", "p", body, false)
	if err != nil {
		t.Fatal(err)
	}
	signature, _ := event.Payload["error_signature"].(string)
	if event.Outcome != "failure" || signature == "" || len([]byte(signature)) > 512 || strings.Contains(signature, "fixture-client-secret") || !strings.Contains(signature, "[REDACTED]") {
		t.Fatalf("unexpected failure signature: %#v", event)
	}
	if _, ok := event.Payload["client_event"]; ok {
		t.Fatalf("raw client event retained without opt-in: %#v", event.Payload)
	}
}
