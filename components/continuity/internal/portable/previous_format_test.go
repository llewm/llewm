package portable

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredObservationsRemainVerifiableDuringTransportConversion(t *testing.T) {
	body, err := os.ReadFile("testdata/format2-records.golden.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var previous Envelope
	if err := json.Unmarshal(bytes.Split(body, []byte{'\n'})[0], &previous); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"contract", "contract_binding", "contract_transition"} {
		previous.RecordType, previous.EnvelopeSHA256 = kind, ""
		unsigned, err := canonicalMarshal(previous)
		if err != nil {
			t.Fatal(err)
		}
		previous.EnvelopeSHA256 = hashHex(unsigned)
		original, err := canonicalMarshal(previous)
		if err != nil {
			t.Fatal(err)
		}
		converted, err := ConvertPreviousEnvelope(original)
		if err != nil {
			t.Fatal(err)
		}
		if converted.Envelope.ContentSHA256 != previous.ContentSHA256 || !bytes.Equal(converted.Envelope.Payload, previous.Payload) || converted.Envelope.RecordType != kind {
			t.Fatal("retired observation changed")
		}
		if !RetiredRecordType(converted.Envelope.RecordType) {
			t.Fatal("converted retired observation became active")
		}
		if err := validateEnvelope(converted.Envelope, converted.Envelope.LineageID); err == nil {
			t.Fatal("retired observation was accepted as an operational record")
		}
	}
}

func TestFormatTwoConversionPreservesHistoricalObservations(t *testing.T) {
	body, err := os.ReadFile("testdata/format2-records.golden.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
		var old Envelope
		if err := json.Unmarshal(line, &old); err != nil {
			t.Fatal(err)
		}
		path := strings.Replace(filepath.ToSlash(MemoryRecordPath(old)), "v3/", "v2/", 1)
		converted, err := ConvertPreviousMemoryFile(path, append(append([]byte(nil), line...), '\n'))
		if err != nil {
			t.Fatal(err)
		}
		new := converted.Envelope
		if new.ContentSHA256 != old.ContentSHA256 || !bytes.Equal(new.Payload, old.Payload) || new.RecordID != old.RecordID || new.RecordRevision != old.RecordRevision || new.RecordedAt != old.RecordedAt || new.LineageID != old.LineageID || new.Authority != old.Authority {
			t.Fatalf("semantic observation changed: old=%+v new=%+v", old, new)
		}
		if new.EnvelopeSHA256 == old.EnvelopeSHA256 || new.Format != "llewm-portable-record" || new.FormatVersion != 3 {
			t.Fatal("transport identity did not change")
		}
		if _, err := DecodeMemoryFile(MemoryRecordPath(new), MemoryFileBytes(converted)); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeMemoryFile(path, append(append([]byte(nil), line...), '\n')); err == nil {
			t.Fatal("ordinary import accepted legacy wire format")
		}
		if _, err := DecodeStoredMemoryEnvelope(line); err != nil {
			t.Fatalf("immutable historical fork observation unreadable: %v", err)
		}
		if _, err := ConvertPreviousMemoryFile(path+".wrong", append(append([]byte(nil), line...), '\n')); err == nil {
			t.Fatal("accepted wrong old path")
		}
		tampered := bytes.Replace(line, []byte(old.ContentSHA256), []byte(strings.Repeat("0", 64)), 1)
		if _, err := ConvertPreviousEnvelope(tampered); err == nil {
			t.Fatal("accepted tampered historical observation")
		}
		if _, err := ConvertPreviousEnvelope(converted.Bytes); err == nil {
			t.Fatal("converter accepted a current-format record as legacy")
		}
	}
}
