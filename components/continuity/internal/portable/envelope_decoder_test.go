package portable

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func envelopeDecoderSeed(t testing.TB) []byte {
	t.Helper()
	e, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RecordType: "checkpoint", RecordSchemaVersion: 1, RecordID: "decoder", RecordedAt: time.Unix(1234567890, 123456789).UTC(), Authority: "observed", Payload: json.RawMessage(`{"nested":[0,true,null,{"😀":"<>&"}]}`), Index: json.RawMessage(`{"title":"Decoder"}`), Refs: []Reference{{Kind: "task", ID: "task"}}})
	if err != nil {
		t.Fatal(err)
	}
	return e.Bytes
}

func assertEnvelopeDecoderReference(t testing.TB, body []byte) {
	t.Helper()
	want, wantErr := decodeTransportEnvelopeWithDecoder(body, false)
	got, err := DecodeTransportEnvelope(body)
	if (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() || err == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("transport decoder differs: got %v, want %v", err, wantErr)
	}
	wantSpec, want, wantErr := decodeMemoryEnvelopeWithDecoder(body, false)
	gotSpec, got, err := decodeMemoryEnvelope(body)
	if (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() || err == nil && (!reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotSpec, wantSpec)) {
		t.Fatalf("memory decoder differs: got %v, want %v", err, wantErr)
	}
}

func TestEnvelopeDecoderMatchesStandard(t *testing.T) {
	seed := envelopeDecoderSeed(t)
	bodies := [][]byte{seed, transportReferenceSeed(t), nil, []byte(`null`), []byte(`[]`), append(bytes.Clone(seed), '\n'), append(bytes.Clone(seed), []byte(` {}`)...), append([]byte(" "), seed...), bytes.Replace(seed, []byte(`"authority":`), []byte(`"unknown":true,"authority":`), 1), bytes.Replace(seed, []byte(`"authority":`), []byte(`"authority":"other","authority":`), 1), bytes.Replace(seed, []byte(`"authority":`), []byte(`"AUTHORITY":`), 1), bytes.Replace(seed, []byte(`"refs":[`), []byte(`"refs":[null,`), 1)}
	for _, from := range []string{`"record_revision":0`, `"recorded_at":"2009-02-13T23:31:30.123456789Z"`, `"😀"`, `"payload":{`} {
		for _, to := range []string{`null`, `-0`, `1e1`, `9007199254740993`, `"\ud800"`, string([]byte{0xff}), `"payload":{"duplicate":1,"duplicate":2,`} {
			bodies = append(bodies, bytes.Replace(seed, []byte(from), []byte(to), 1))
		}
	}
	// Both legitimate complex JSON and adversarial depth keep the standard
	// reader's behavior. The accelerator must never recursively inspect them.
	for _, depth := range []int{126, 127, 128, 129, 10001} {
		body := []byte(`{"payload":` + strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth) + `}`)
		bodies = append(bodies, body)
	}
	for _, payload := range []string{`{"nested":` + strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129) + `}`, `{"text":"` + strings.Repeat("{[", 129) + `"}`} {
		spec, _, err := decodeMemoryEnvelopeWithDecoder(seed, false)
		if err != nil {
			t.Fatal(err)
		}
		spec.Payload = json.RawMessage(payload)
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := decodeMemoryEnvelope(encoded.Bytes); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, encoded.Bytes)
	}
	for _, body := range bodies {
		assertEnvelopeDecoderReference(t, body)
	}
}

func TestEnvelopeDecoderOwnsReturnedBuffers(t *testing.T) {
	body := envelopeDecoderSeed(t)
	wantSpec, want, err := decodeMemoryEnvelopeWithDecoder(body, false)
	if err != nil {
		t.Fatal(err)
	}
	gotSpec, got, err := decodeMemoryEnvelope(body)
	if err != nil {
		t.Fatal(err)
	}
	wantTransport, err := decodeTransportEnvelopeWithDecoder(body, false)
	if err != nil {
		t.Fatal(err)
	}
	gotTransport, err := DecodeTransportEnvelope(body)
	if err != nil {
		t.Fatal(err)
	}
	for i := range body {
		body[i] = 'x'
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotSpec, wantSpec) || !reflect.DeepEqual(gotTransport, wantTransport) {
		t.Fatal("input mutation changed returned envelope")
	}
}

func FuzzEnvelopeDecoderMatchesStandard(f *testing.F) {
	for _, body := range [][]byte{envelopeDecoderSeed(f), transportReferenceSeed(f), []byte(`{}`), []byte(`null`), []byte(strings.Repeat("[", 129))} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) { assertEnvelopeDecoderReference(t, body) })
}

func FuzzEnvelopeDecoderStrings(f *testing.F) {
	f.Add("<>&😀", "identity", "observed")
	f.Add("\x00\xff\\\"", "\ud7ff\U00010000\ue000", "caller_asserted")
	f.Fuzz(func(t *testing.T, payload, id, authority string) {
		if len(payload)+len(id)+len(authority) > 64<<10 {
			t.Skip()
		}
		spec, _, err := decodeMemoryEnvelopeWithDecoder(envelopeDecoderSeed(t), false)
		if err != nil {
			t.Fatal(err)
		}
		spec.Payload, err = json.Marshal(map[string]string{"text": payload})
		if err != nil {
			t.Fatal(err)
		}
		spec.RecordID, spec.Authority = id, authority
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			return
		}
		assertEnvelopeDecoderReference(t, encoded.Bytes)
	})
}

func BenchmarkEnvelopeDecoders(b *testing.B) {
	for _, size := range []int{1024, 64 << 10} {
		var spec EnvelopeSpec
		var err error
		spec, _, err = decodeMemoryEnvelopeWithDecoder(envelopeDecoderSeed(b), false)
		if err != nil {
			b.Fatal(err)
		}
		spec.Payload, err = json.Marshal(map[string]string{"observation": strings.Repeat("Observation <>&😀. ", size/24)})
		if err != nil {
			b.Fatal(err)
		}
		encoded, err := EncodeEnvelope(spec)
		if err != nil {
			b.Fatal(err)
		}
		for _, mode := range []string{"standard", "accelerated"} {
			b.Run(strings.Join([]string{strconv.Itoa(size), mode}, "/"), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if _, err := decodeTransportEnvelopeWithDecoder(encoded.Bytes, mode == "accelerated"); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
