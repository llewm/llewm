package portable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Preserve the former generic canonicalization/hash path as an independent
// reference for the fixed-layout reconstruction. Shared field validation still
// runs on both paths; schema knowledge is deliberately not required here.
func referenceTransportEnvelope(body []byte) (EncodedEnvelope, error) {
	if len(body) > MaxPayloadBytes+MaxIndexBytes+(64<<10) {
		return EncodedEnvelope{}, fmt.Errorf("transport envelope exceeds bounds")
	}
	line := bytes.TrimSuffix(body, []byte("\n"))
	var e Envelope
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return EncodedEnvelope{}, err
	}
	if !transportRecordTypePattern.MatchString(e.RecordType) || e.RecordSchemaVersion < 1 {
		return EncodedEnvelope{}, fmt.Errorf("invalid transport record type/schema")
	}
	if err := validateEnvelopeStructure(e, e.LineageID, "llewm-portable-record", PortableFormatVersion, false); err != nil {
		return EncodedEnvelope{}, err
	}
	canonical, err := canonicalMarshal(e)
	if err != nil || !bytes.Equal(canonical, line) {
		return EncodedEnvelope{}, fmt.Errorf("noncanonical transport envelope")
	}
	return EncodedEnvelope{Envelope: e, Bytes: canonical}, nil
}

func transportReferenceSeed(t testing.TB) []byte {
	t.Helper()
	e := Envelope{Format: "llewm-portable-record", FormatVersion: PortableFormatVersion, LineageID: "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RecordType: "future_record", RecordSchemaVersion: 999, RecordID: "reference", RecordedAt: time.Unix(1234567890, 123456789).UTC().Format(time.RFC3339Nano), Authority: "observed", Index: json.RawMessage(`{"title":"<>&😀"}`), Payload: json.RawMessage(`{"nested":[0,true,null,{"😀":"<>&"}],"envelope_sha256":"a quoted value"}`), Refs: []Reference{{Kind: "task", ID: "task"}}}
	// Use canonical payload bytes for the content hash, independently of the
	// fixed envelope writer and its hash-member removal helper.
	payload, err := canonicalObject("payload", e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	e.Payload = payload
	e.ContentSHA256 = hashHex(e.Payload)
	without, err := canonicalMarshal(e)
	if err != nil {
		t.Fatal(err)
	}
	e.EnvelopeSHA256 = hashHex(without)
	wire, err := canonicalMarshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func assertTransportReference(t testing.TB, body []byte) {
	t.Helper()
	want, wantErr := referenceTransportEnvelope(body)
	got, err := DecodeTransportEnvelope(body)
	if (err == nil) != (wantErr == nil) {
		t.Fatalf("acceptance differs: optimized=%v reference=%v", err, wantErr)
	}
	if err == nil && !reflect.DeepEqual(got, want) {
		t.Fatal("decoded envelope or bytes differ")
	}
	if err != nil && err.Error() != wantErr.Error() {
		t.Fatalf("rejection differs: optimized=%v reference=%v", err, wantErr)
	}
}

func TestTransportReconstructionMatchesReference(t *testing.T) {
	seed := transportReferenceSeed(t)
	for _, body := range [][]byte{seed, append(bytes.Clone(seed), '\n'), append(bytes.Clone(seed), '\n', '\n'), append([]byte(" "), seed...), append(bytes.Clone(seed), []byte(` {}`)...), bytes.Replace(seed, []byte(`"authority":`), []byte(`"authority":"observed","authority":`), 1), bytes.Replace(seed, []byte(`{"authority":`), []byte(`{"unexpected":true,"authority":`), 1), bytes.Replace(seed, []byte(`"record_revision":0`), []byte(`"record_revision":-0`), 1), bytes.Replace(seed, []byte(`"record_schema_version":999`), []byte(`"record_schema_version":1e3`), 1), bytes.Replace(seed, []byte("<>&"), []byte(`\u003c\u003e\u0026`), 1), bytes.Replace(seed, []byte("😀"), []byte{0xff}, 1), bytes.Replace(seed, []byte("😀"), []byte(`\ud800`), 1), bytes.Replace(seed, []byte(`"payload":{`), []byte(`"payload":{"duplicate":1,"duplicate":2,`), 1)} {
		assertTransportReference(t, body)
	}
	// Forge byte-consistent hashes with out-of-profile integers. The generic
	// reference rejects these even for unknown schemas; the fast path must too.
	for _, field := range []string{"record_revision", "record_schema_version"} {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(seed, &object); err != nil {
			t.Fatal(err)
		}
		delete(object, "envelope_sha256")
		object[field] = json.RawMessage("9007199254740992")
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(object); err != nil {
			t.Fatal(err)
		}
		object["envelope_sha256"], _ = json.Marshal(hashHex(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))))
		buf.Reset()
		if err := encoder.Encode(object); err != nil {
			t.Fatal(err)
		}
		body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		assertTransportReference(t, body)
		if _, err := DecodeTransportEnvelope(body); err == nil {
			t.Fatal("out-of-range integer accepted")
		}
	}
}

func FuzzTransportReconstructionMatchesReference(f *testing.F) {
	seed := transportReferenceSeed(f)
	for _, body := range [][]byte{seed, []byte(`{}`), []byte(`null`), []byte(strings.Repeat("[", 100)), append(bytes.Clone(seed), '\n')} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) { assertTransportReference(t, body) })
}

func BenchmarkTransportEnvelopeDecoding(b *testing.B) {
	for _, size := range []int{1024, 64 << 10} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			var e Envelope
			if err := json.Unmarshal(transportReferenceSeed(b), &e); err != nil {
				b.Fatal(err)
			}
			e.Payload, _ = json.Marshal(map[string]string{"observation": strings.Repeat("Observation <>&😀. ", size/24)})
			var err error
			e.Payload, err = canonicalObject("payload", e.Payload)
			if err != nil {
				b.Fatal(err)
			}
			e.ContentSHA256 = hashHex(e.Payload)
			e.EnvelopeSHA256 = ""
			without, err := canonicalMarshal(e)
			if err != nil {
				b.Fatal(err)
			}
			e.EnvelopeSHA256 = hashHex(without)
			body, err := canonicalMarshal(e)
			if err != nil {
				b.Fatal(err)
			}
			for _, mode := range []string{"reference", "reconstructed"} {
				b.Run(mode, func(b *testing.B) {
					decode := referenceTransportEnvelope
					if mode == "reconstructed" {
						decode = DecodeTransportEnvelope
					}
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if _, err := decode(body); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
