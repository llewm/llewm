package portable

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestSnapshotDecodeCacheOwnsValidatedBytes(t *testing.T) {
	ctx, work := syncwork.WithWorkMetrics(WithEnvelopeDecodeCache(context.Background()))
	object, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:abababababababababababababababab", RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: "record", RecordedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Index: []byte(`{"title":"Observation"}`), Refs: []Reference{{Kind: "task", ID: "task"}}, Payload: []byte(`{"title":"Observation"}`)})
	if err != nil {
		t.Fatal(err)
	}
	wantSpec, wantEncoded, err := decodeMemoryEnvelope(object.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Clone(object.Bytes)
	for pass := 0; pass < 3; pass++ {
		spec, encoded, err := decodeSnapshotEnvelope(ctx, body)
		if err != nil || !reflect.DeepEqual(spec, wantSpec) || !reflect.DeepEqual(encoded, wantEncoded) {
			t.Fatalf("pass %d changed result: %v", pass, err)
		}
		// Neither the first result nor a hit may expose mutable cached buffers.
		spec.Index[0] = '!'
		spec.Payload[0] = '!'
		spec.Refs[0].ID = "changed"
		encoded.Bytes[0] = '!'
		encoded.Envelope.Index[0] = '!'
		encoded.Envelope.Payload[0] = '!'
		encoded.Envelope.Refs[0].ID = "changed"
	}
	// The key is an index, not an integrity receipt. Even a simulated collision
	// must compare exact bytes and then invoke the full decoder.
	cache := ctx.Value(decodeCacheKey{}).(*envelopeDecodeCache)
	for key := range cache.records {
		bad := cache.records[key]
		bad.encoded.Bytes[0] = '!'
		cache.records[key] = bad
	}
	if _, encoded, err := decodeSnapshotEnvelope(ctx, body); err != nil || !bytes.Equal(encoded.Bytes, wantEncoded.Bytes) {
		t.Fatalf("collision bypassed decoder: %v", err)
	}
	body[0] = '!'
	if _, _, err := decodeSnapshotEnvelope(ctx, body); err == nil {
		t.Fatal("altered input accepted")
	}
	if work.Counts()["snapshot_decode_cache_hits"] != 2 {
		t.Fatalf("unexpected reuse: %v", work.Counts())
	}
}

func TestSnapshotDecodeCacheBoundsAndIsolation(t *testing.T) {
	for _, mode := range []string{"bytes", "records"} {
		t.Run(mode, func(t *testing.T) {
			ctx, work := syncwork.WithWorkMetrics(WithEnvelopeDecodeCache(context.Background()))
			cache := ctx.Value(decodeCacheKey{}).(*envelopeDecodeCache)
			if mode == "bytes" {
				cache.bytes = maxDecodeCacheBytes
			} else {
				for i := 0; i < maxDecodeCacheRecords; i++ {
					var key [32]byte
					key[0] = byte(i)
					key[1] = byte(i >> 8)
					cache.records[key] = cachedEnvelope{}
				}
			}
			object, err := EncodeEnvelope(EnvelopeSpec{LineageID: "lineage:abababababababababababababababab", RecordType: "decision", RecordSchemaVersion: 1, RecordID: "bounded", RecordedAt: time.Now(), Authority: "caller_asserted", Payload: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, _, err := decodeSnapshotEnvelope(ctx, object.Bytes); err != nil {
					t.Fatal(err)
				}
			}
			if work.Counts()["snapshot_decode_cache_hits"] != 0 || work.Counts()["snapshot_decode_cache_misses"] != 2 {
				t.Fatalf("full cache affected validation: %v", work.Counts())
			}
			other := WithEnvelopeDecodeCache(ctx).Value(decodeCacheKey{}).(*envelopeDecodeCache)
			if len(other.records) != 0 || other.bytes != 0 {
				t.Fatal("separate operations share cache")
			}
			if _, _, err := decodeSnapshotEnvelope(ctx, []byte(strings.Repeat("x", 32))); err == nil {
				t.Fatal("full cache accepted invalid input")
			}
		})
	}
}
