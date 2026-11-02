package portable

import (
	"testing"
	"time"
)

func TestEncodedEnvelopeTimeRoundTripsThroughTransport(t *testing.T) {
	for _, year := range []int{-1, 0, 1, 2026, 9999, 10000} {
		spec := EnvelopeSpec{LineageID: "lineage:abababababababababababababababab", RecordType: "decision", RecordSchemaVersion: 1, RecordID: "time", Authority: "observed", RecordedAt: time.Date(year, 7, 2, 3, 4, 5, 123, time.UTC), Payload: []byte(`{}`)}
		encoded, err := EncodeEnvelope(spec)
		if year < 0 || year > 9999 {
			if err == nil {
				t.Fatalf("year %d produced a transport-invalid timestamp", year)
			}
			continue
		}
		if err != nil {
			t.Fatalf("year %d: %v", year, err)
		}
		if _, err := DecodeTransportEnvelope(encoded.Bytes); err != nil {
			t.Fatalf("year %d encoded a rejected record: %v", year, err)
		}
	}
}
