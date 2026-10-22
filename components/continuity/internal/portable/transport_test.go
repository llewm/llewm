package portable

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestOpaqueTransportValidationDoesNotWeakenSemanticReaders(t *testing.T) {
	e, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RecordType: "decision", RecordSchemaVersion: 1, RecordID: "opaque", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: json.RawMessage(`{"title":"Portable observation"}`)})
	if err != nil {
		t.Fatal(err)
	}
	unknown := e.Envelope
	unknown.RecordSchemaVersion = 999
	unknown.EnvelopeSHA256 = ""
	body, err := canonicalMarshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	unknown.EnvelopeSHA256 = hashHex(body)
	body, err = canonicalMarshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTransportEnvelope(body)
	if err != nil || !bytes.Equal(decoded.Bytes, body) || SupportedEnvelope(decoded.Envelope) {
		t.Fatalf("opaque bytes not preserved: %v", err)
	}
	if _, err := DecodeMemoryEnvelope(body); err == nil {
		t.Fatal("ordinary semantic reader accepted unsupported schema")
	}
	corrupted := bytes.Replace(body, []byte("Portable observation"), []byte("Tampered observation"), 1)
	for _, invalid := range [][]byte{corrupted, append(bytes.Clone(body), []byte(` {}`)...), []byte("{}"), bytes.Replace(body, []byte(`"record_schema_version":999`), []byte(`"record_schema_version":999,"record_schema_version":999`), 1)} {
		if _, err := DecodeTransportEnvelope(invalid); err == nil {
			t.Fatal("opaque relay bypassed structural/hash validation")
		}
	}
}
