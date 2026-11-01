package portable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

const maxDecodeCacheBytes = 64 << 20
const maxDecodeCacheRecords = 8192

type decodeCacheKey struct{}
type cachedEnvelope struct {
	encoded  EncodedEnvelope
	recorded time.Time
}
type envelopeDecodeCache struct {
	mu      sync.Mutex
	records map[[32]byte]cachedEnvelope
	bytes   int
}

// WithEnvelopeDecodeCache bounds immutable byte-validation reuse to one caller
// operation. It never certifies database state: each snapshot must still read
// its rows and perform all operational, identity, recovery and transaction
// checks. Only exact canonical bytes previously validated by this process can
// hit; returned buffers are independent of the private cached observation.
func WithEnvelopeDecodeCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, decodeCacheKey{}, &envelopeDecodeCache{records: make(map[[32]byte]cachedEnvelope)})
}

func cloneEncodedEnvelope(encoded EncodedEnvelope) EncodedEnvelope {
	encoded.Bytes = bytes.Clone(encoded.Bytes)
	encoded.Envelope.Index = bytes.Clone(encoded.Envelope.Index)
	encoded.Envelope.Payload = bytes.Clone(encoded.Envelope.Payload)
	encoded.Envelope.Refs = slices.Clone(encoded.Envelope.Refs)
	return encoded
}

func decodeSnapshotEnvelope(ctx context.Context, body []byte) (EnvelopeSpec, EncodedEnvelope, error) {
	cache, ok := ctx.Value(decodeCacheKey{}).(*envelopeDecodeCache)
	if !ok {
		return decodeMemoryEnvelope(body)
	}
	hash := sha256.Sum256(body)
	cache.mu.Lock()
	saved, found := cache.records[hash]
	cache.mu.Unlock()
	if found && bytes.Equal(saved.encoded.Bytes, body) {
		encoded := cloneEncodedEnvelope(saved.encoded)
		e := encoded.Envelope
		spec := EnvelopeSpec{LineageID: e.LineageID, RecordType: e.RecordType, RecordSchemaVersion: e.RecordSchemaVersion, RecordID: e.RecordID, RecordRevision: e.RecordRevision, RecordedAt: saved.recorded, Authority: e.Authority, Index: bytes.Clone(e.Index), Refs: slices.Clone(e.Refs), Payload: bytes.Clone(e.Payload)}
		syncwork.RecordWork(ctx, "snapshot_decode_cache_hits", 1)
		return spec, encoded, nil
	}
	spec, encoded, err := decodeMemoryEnvelope(body)
	if err != nil {
		return spec, encoded, err
	}
	syncwork.RecordWork(ctx, "snapshot_decode_cache_misses", 1)
	// Canonical bytes bound the decoded strings, payload/index and references.
	// Charge three wire copies plus fixed per-entry overhead conservatively;
	// refusal affects performance only and never bypasses the full decoder.
	cost := 3*len(body) + 512
	cache.mu.Lock()
	if _, exists := cache.records[hash]; !exists && len(cache.records) < maxDecodeCacheRecords && cost <= maxDecodeCacheBytes-cache.bytes {
		cache.records[hash] = cachedEnvelope{encoded: cloneEncodedEnvelope(encoded), recorded: spec.RecordedAt}
		cache.bytes += cost
	}
	cache.mu.Unlock()
	return spec, encoded, nil
}
