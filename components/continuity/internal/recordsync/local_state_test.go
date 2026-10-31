package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Reference encoding deliberately keeps the old independent-state algorithm.
func referenceLocalState(local map[string]portable.EncodedEnvelope, excluded map[string]bool) string {
	var keys []string
	for key := range local {
		if !excluded[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var body bytes.Buffer
	for _, key := range keys {
		_ = binary.Write(&body, binary.BigEndian, uint64(len(key)))
		body.WriteString(key)
		sum := sha256.Sum256(local[key].Bytes)
		body.Write(sum[:])
	}
	sum := sha256.Sum256(body.Bytes())
	return hex.EncodeToString(sum[:])
}

func TestLocalStateHashesPreserveBothProofsWithOnePayloadPass(t *testing.T) {
	local := map[string]portable.EncodedEnvelope{
		"z":      {Bytes: []byte("old payload")},
		"a\x00b": {Bytes: []byte("arriving payload")},
		"é":      {Bytes: []byte{}},
	}
	for _, excluded := range []map[string]bool{nil, {"absent": true}, {"a\x00b": true}, {"z": true, "a\x00b": true, "é": true}} {
		ctx, metrics := syncwork.WithWorkMetrics(context.Background())
		prior, current := localStateHashes(ctx, local, excluded, true)
		if prior != referenceLocalState(local, excluded) || current != referenceLocalState(local, nil) {
			t.Fatalf("proof encoding changed for exclusions %v", excluded)
		}
		counts := metrics.Counts()
		if counts["local_state_records_visited"] != 3 || counts["local_state_records_hashed"] != 3 || counts["local_state_payload_bytes_hashed"] != int64(len("old payloadarriving payload")) {
			t.Fatalf("payloads were not measured once: %v", counts)
		}
		ctx, metrics = syncwork.WithWorkMetrics(context.Background())
		single, unused := localStateHashes(ctx, local, excluded, false)
		if single != prior || unused != "" {
			t.Fatal("single proof differs")
		}
		if metrics.Counts()["local_state_records_visited"] != 3 {
			t.Fatal("single proof did not visit complete membership")
		}
	}
	prior, current := localStateHashes(context.Background(), nil, nil, true)
	if prior != referenceLocalState(nil, nil) || current != prior {
		t.Fatal("empty state encoding changed")
	}
}
