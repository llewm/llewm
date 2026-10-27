package recordsync

import (
	"bytes"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestBlockRoundTripAndIntegrity(t *testing.T) {
	a := fixture(t)
	b, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "second", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := EncodeBlock(lineage, []portable.EncodedEnvelope{a, b})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeBlock(lineage, []portable.EncodedEnvelope{b, a})
	if err != nil || first.Hash != second.Hash || !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatalf("input order changed block: %v", err)
	}
	decoded, err := DecodeBlock(lineage, first.Hash, first.Bytes)
	if err != nil || len(decoded.Objects) != 2 {
		t.Fatalf("block round trip: %+v %v", decoded, err)
	}
	if _, err := DecodeBlock(lineage, first.Hash, first.Bytes[:len(first.Bytes)-1]); err == nil {
		t.Fatal("accepted incomplete block")
	}
	if _, err := DecodeBlock(lineage, Hash("wrong"), first.Bytes); err == nil {
		t.Fatal("accepted wrong block hash")
	}
	if _, err := DecodeBlock("lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", first.Hash, first.Bytes); err == nil {
		t.Fatal("accepted wrong lineage")
	}
	if _, err := EncodeBlock(lineage, []portable.EncodedEnvelope{a, a}); err == nil {
		t.Fatal("accepted duplicate envelope")
	}
	corrupt := append([]byte(nil), first.Bytes...)
	corrupt[len(corrupt)-2] ^= 1
	if _, err := DecodeBlock(lineage, "", corrupt); err == nil {
		t.Fatal("accepted corrupt envelope")
	}
}
