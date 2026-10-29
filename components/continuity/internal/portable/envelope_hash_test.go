package portable

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The general canonical encoder is the independent reference for both hash
// input and final wire bytes, including optional fields and hostile strings.
func FuzzEnvelopeHashMatchesTwoPassEncoding(f *testing.F) {
	for _, value := range []string{"plain", `,"envelope_sha256":"` + strings.Repeat("0", 64) + `"`, "<>&\u2028😀\x00\xff"} {
		f.Add(value, true)
		f.Add(value, false)
	}
	f.Fuzz(func(t *testing.T, text string, optional bool) {
		if len(text) > MaxIndexBytes/8 {
			t.Skip()
		}
		payload, err := json.Marshal(map[string]any{"envelope_sha256": text, "nested": map[string]string{"content_sha256": text}})
		if err != nil {
			t.Fatal(err)
		}
		spec := EnvelopeSpec{
			LineageID: "lineage:0123456789abcdef0123456789abcdef", RecordType: "work_narrative", RecordSchemaVersion: 1,
			RecordID: "id:" + text, Authority: "authority:" + text, RecordedAt: time.Unix(1234567890, 123456789), Payload: payload,
		}
		if optional {
			spec.Index, _ = json.Marshal(map[string]string{"title": text})
			spec.Refs = []Reference{{Kind: "task", ID: "id:" + text}, {Kind: "commit", ID: "commit:" + text}}
		}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			t.Fatal(err)
		}
		reference := encoded.Envelope
		reference.EnvelopeSHA256 = ""
		withoutHash, err := canonicalMarshal(reference)
		if err != nil {
			t.Fatal(err)
		}
		reference.EnvelopeSHA256 = hashHex(withoutHash)
		want, err := canonicalMarshal(reference)
		if err != nil {
			t.Fatal(err)
		}
		if reference.EnvelopeSHA256 != encoded.Envelope.EnvelopeSHA256 || !bytes.Equal(want, encoded.Bytes) {
			t.Fatalf("single-pass hash/bytes differ from two-pass reference")
		}
	})
}

func TestEnvelopeHashPreservesCanonicalSizeLimit(t *testing.T) {
	// HTML escaping expands these strings in json.Marshal before the canonical
	// parser checks its byte limit, even though final canonical bytes are small.
	spec := EnvelopeSpec{
		LineageID: "lineage:0123456789abcdef0123456789abcdef", RecordType: "work_narrative", RecordSchemaVersion: 1,
		RecordID: "large", Authority: strings.Repeat("<", MaxCanonicalBytes/6), RecordedAt: time.Unix(1234567890, 0), Payload: json.RawMessage(`{}`),
	}
	if _, err := EncodeEnvelope(spec); err == nil || !strings.Contains(err.Error(), "canonical JSON exceeds") {
		t.Fatalf("expected canonical size rejection, got %v", err)
	}
}
