package portable

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func projectionFixture(t *testing.T, kind string) EncodedEnvelope {
	t.Helper()
	spec := EnvelopeSpec{LineageID: "lineage:abababababababababababababababab", RecordType: kind, RecordSchemaVersion: 1, RecordID: "historical-record", Authority: "caller_asserted", RecordedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	prose := "Restore https://fixture-user:fixture-password@example.test/history"
	var payload any
	switch kind {
	case "checkpoint":
		payload = checkpointPayload{Task: "Historical recovery", Status: "completed", Summary: prose, NextAction: "Complete", OperationalSchemaVersion: 1, Metadata: map[string]any{"count": json.Number("9007199254740991")}}
		spec.Index = []byte(`{"title":"Historical recovery","status":"completed"}`)
	case "checkpoint_action":
		payload = checkpointActionPayload{SourceCheckpointID: "parent", Description: prose}
		spec.Index, _ = json.Marshal(envelopeIndex{Title: prose})
		spec.Refs = []Reference{{Kind: "checkpoint", ID: "parent"}}
	case "decision":
		payload = decisionPayload{Title: "Recovery decision", Rationale: prose, Disposition: "accepted", Actor: "human", ActorType: "human", ReviewSource: "chat", Links: []decisionLinkPayload{}, OperationalSchemaVersion: 1}
		spec.Index = []byte(`{"title":"Recovery decision","status":"accepted"}`)
	case "resource_note":
		payload = resourceNotePayload{ResourceID: "host", Text: prose, Creator: "human", ObservedAt: "2026-09-29T12:00:00Z", OperationalSchemaVersion: 1}
		spec.Index = []byte(`{"title":"host"}`)
		spec.Refs = []Reference{{Kind: "resource", ID: "host"}}
	default:
		t.Fatal("unsupported fixture")
	}
	spec.Payload, _ = json.Marshal(payload)
	encoded, err := EncodeEnvelope(spec)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRedactionProjectionPreservesSourceAndIdentity(t *testing.T) {
	for _, kind := range []string{"checkpoint", "checkpoint_action", "decision", "resource_note"} {
		t.Run(kind, func(t *testing.T) {
			source := projectionFixture(t, kind)
			before := bytes.Clone(source.Bytes)
			got, err := PreviewRedactionProjection(source.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if got.SourceSHA256 != source.Envelope.EnvelopeSHA256 || !bytes.Equal(source.Bytes, before) {
				t.Fatal("source changed")
			}
			if bytes.Contains(got.Projected.Bytes, []byte("fixture-user")) || bytes.Contains(got.Projected.Bytes, []byte("fixture-password")) {
				t.Fatal("credential escaped projection")
			}
			if got.Projected.Envelope.RecordID != source.Envelope.RecordID || got.Projected.Envelope.Authority != source.Envelope.Authority || got.Projected.Envelope.RecordedAt != source.Envelope.RecordedAt {
				t.Fatal("identity/provenance changed")
			}
			if _, err := PreviewRedactionProjection(got.Projected.Bytes); err == nil {
				t.Fatal("accepted projection of already-safe input")
			}
			if kind == "checkpoint" && !bytes.Contains(got.Projected.Bytes, []byte("9007199254740991")) {
				t.Fatal("JSON number changed")
			}
			again, err := PreviewRedactionProjection(before)
			if err != nil || !bytes.Equal(again.Projected.Bytes, got.Projected.Bytes) {
				t.Fatal("projection is not deterministic")
			}
		})
	}
}

func TestRedactionProjectionRefusesUnrelatedChanges(t *testing.T) {
	original := projectionFixture(t, "checkpoint")
	for _, tc := range []struct {
		name string
		edit func(*EnvelopeSpec)
	}{
		{"invalid status", func(s *EnvelopeSpec) {
			s.Payload = bytes.ReplaceAll(s.Payload, []byte(`"completed"`), []byte(`"unknown"`))
		}},
		{"schema", func(s *EnvelopeSpec) {
			s.Payload = bytes.ReplaceAll(s.Payload, []byte(`"operational_schema_version":1`), []byte(`"operational_schema_version":9`))
		}},
		{"index mismatch", func(s *EnvelopeSpec) { s.Index = []byte(`{"title":"Different","status":"completed"}`) }},
		{"reference rename", func(s *EnvelopeSpec) {
			s.Refs = []Reference{{Kind: "task", ID: "https://fixture-secret@example.test/task"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := DecodeMemoryEnvelope(original.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&spec)
			changed, err := EncodeEnvelope(spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PreviewRedactionProjection(changed.Bytes); err == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
	damaged := bytes.Replace(original.Bytes, []byte("fixture-password"), []byte("changed-password"), 1)
	if _, err := PreviewRedactionProjection(damaged); err == nil {
		t.Fatal("accepted corrupted hash")
	}
}

func TestProjectionJSONRejectsKeyCollapseAndRedactsMetadata(t *testing.T) {
	if _, err := projectionJSON([]byte(`{"token=one":1,"token=two":2}`)); err == nil {
		t.Fatal("accepted key collapse")
	}
	safe, err := projectionJSON([]byte(`{"environment":{"ordinary":"fixture-secret"},"nested":["--token fixture-value"]}`))
	if err != nil || strings.Contains(string(safe), "fixture-") {
		t.Fatalf("unsafe structural projection: %v", err)
	}
}
