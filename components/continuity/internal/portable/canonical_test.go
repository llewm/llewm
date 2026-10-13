package portable

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalizeJSONProfile(t *testing.T) {
	input := []byte("{\"z\":0,\"😀\":1,\"a\":{\"line\":\"x\\ny\",\"control\":\"\\u000f\"},\"€\":2}")
	want := "{\"a\":{\"control\":\"\\u000f\",\"line\":\"x\\ny\"},\"z\":0,\"€\":2,\"😀\":1}"
	got, err := CanonicalizeJSON(input)
	if err != nil || string(got) != want {
		t.Fatalf("canonical JSON mismatch:\nwant %s\n got %s\nerr %v", want, got, err)
	}
	escapedPair, err := CanonicalizeJSON([]byte(`{"emoji":"\ud83d\ude00","separator":"\u2028"}`))
	if err != nil || string(escapedPair) != "{\"emoji\":\"😀\",\"separator\":\" \"}" {
		t.Fatalf("valid Unicode escapes did not canonicalize: %s %v", escapedPair, err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"a":1,"a":2}`), []byte(`{"a":1.0}`), []byte(`{"a":01}`),
		[]byte(`{"a":9007199254740992}`), []byte(`{"a":"\ud800"}`), append([]byte(`{"a":"`), 0xff, '"', '}'),
		[]byte(`{} {}`),
	} {
		if _, err := CanonicalizeJSON(invalid); err == nil {
			t.Fatalf("accepted invalid canonical profile input %q", invalid)
		}
	}
}

func TestBuildBundleIsStableAndCanonical(t *testing.T) {
	lineage := "lineage:0123456789abcdef0123456789abcdef"
	recorded := time.Date(2026, 9, 18, 12, 0, 0, 123456789, time.UTC)
	records := []EnvelopeSpec{
		{LineageID: lineage, RecordType: "work_item", RecordSchemaVersion: 1, RecordID: "second", RecordedAt: recorded, Authority: "caller_asserted", Payload: json.RawMessage(`{"title":"Later","tags":["a","z"]}`)},
		{LineageID: lineage, RecordType: "work_narrative", RecordSchemaVersion: 1, RecordID: "first", RecordedAt: recorded, Authority: "caller_asserted", Index: json.RawMessage(`{"title":"First","tags":[]}`), Refs: []Reference{{Kind: "task", ID: "portable"}, {Kind: "commit", ID: "abc"}}, Payload: json.RawMessage(`{"body":"why","title":"First"}`)},
	}
	spec := BundleSpec{
		ExportID: "export:fixture-a", LineageID: lineage,
		SourceInstanceID: "instance:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: recorded,
		SourceDatabaseSchema: "014_portable_identity_effort.sql", SelectionProfile: "curated-v1",
		RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("b", 64)},
		Omissions:        []Omission{{RecordFamily: "usage", Reason: "profile_excluded"}, {RecordFamily: "events", Reason: "profile_excluded"}},
	}
	first, err := BuildBundle(spec, records)
	if err != nil {
		t.Fatal(err)
	}
	secondSpec := spec
	secondSpec.ExportID = "export:fixture-b"
	secondSpec.CreatedAt = recorded.Add(time.Hour)
	second, err := BuildBundle(secondSpec, []EnvelopeSpec{records[1], records[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.RecordsBytes, second.RecordsBytes) || first.Manifest.RecordSetSHA256 != second.Manifest.RecordSetSHA256 {
		t.Fatal("unchanged semantic records did not produce stable record bytes and set hash")
	}
	if bytes.Equal(first.ManifestBytes, second.ManifestBytes) {
		t.Fatal("export identity and time should change manifest bytes")
	}
	if len(first.Records) != 2 || first.Records[0].Envelope.RecordType != "work_item" || first.Manifest.RecordCounts["work_item"] != 1 || first.Manifest.RecordCounts["work_narrative"] != 1 {
		t.Fatalf("unexpected deterministic bundle: %#v", first)
	}
	for _, value := range [][]byte{first.ManifestBytes, first.Records[0].Bytes, first.Records[1].Bytes} {
		canonical, err := CanonicalizeJSON(value)
		if err != nil || !bytes.Equal(canonical, value) {
			t.Fatalf("output was not canonical: %v\n%s", err, value)
		}
	}
	manifestGolden, err := os.ReadFile(filepath.Join("testdata", "manifest.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestGolden = bytes.TrimSuffix(manifestGolden, []byte("\n"))
	recordsGolden, err := os.ReadFile(filepath.Join("testdata", "records.golden.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(filepath.Join("testdata", "manifest.golden.json"), append(append([]byte{}, first.ManifestBytes...), '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("testdata", "records.golden.jsonl"), first.RecordsBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		manifestGolden, recordsGolden = first.ManifestBytes, first.RecordsBytes
	}
	if !bytes.Equal(first.ManifestBytes, manifestGolden) {
		t.Fatalf("manifest golden mismatch:\nwant %s\n got %s", manifestGolden, first.ManifestBytes)
	}
	if !bytes.Equal(first.RecordsBytes, recordsGolden) {
		t.Fatalf("records golden mismatch:\nwant %s\n got %s", recordsGolden, first.RecordsBytes)
	}
	validated, err := ValidateBundle(first.ManifestBytes, first.RecordsBytes)
	if err != nil || len(validated.Records) != 2 {
		t.Fatalf("golden bundle did not validate: %#v %v", validated, err)
	}
}

func TestBuildBundleRejectsAmbiguousRecords(t *testing.T) {
	lineage := "lineage:0123456789abcdef0123456789abcdef"
	record := EnvelopeSpec{LineageID: lineage, RecordType: "work_item", RecordSchemaVersion: 1, RecordID: "same", RecordedAt: time.Now(), Authority: "caller_asserted", Payload: json.RawMessage(`{}`)}
	spec := BundleSpec{ExportID: "export:test", LineageID: lineage, SourceInstanceID: "instance:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now(), SourceDatabaseSchema: "014.sql", SelectionProfile: "curated-v1", RedactionProfile: RedactionProfile{ID: "default", Version: 1, SHA256: strings.Repeat("a", 64)}}
	if _, err := BuildBundle(spec, []EnvelopeSpec{record, record}); err == nil || !strings.Contains(err.Error(), "duplicate record identity") {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
	record.Payload = json.RawMessage(`[]`)
	if _, err := BuildBundle(spec, []EnvelopeSpec{record}); err == nil || !strings.Contains(err.Error(), "payload must be a JSON object") {
		t.Fatalf("expected payload-object rejection, got %v", err)
	}
	record.Payload = json.RawMessage(`{"value":1.5}`)
	if _, err := BuildBundle(spec, []EnvelopeSpec{record}); err == nil || !strings.Contains(err.Error(), "does not support floats") {
		t.Fatalf("expected float rejection, got %v", err)
	}
	record.Payload = json.RawMessage(`{}`)
	record.Index = json.RawMessage(`{"tags":["z","a"]}`)
	if _, err := BuildBundle(spec, []EnvelopeSpec{record}); err == nil || !strings.Contains(err.Error(), "sorted and unique") {
		t.Fatalf("expected set-like index ordering rejection, got %v", err)
	}
}

func TestValidateBundleRejectsTamperingAndUnknownSchema(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("testdata", "manifest.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest = bytes.TrimSuffix(manifest, []byte("\n"))
	records, err := os.ReadFile(filepath.Join("testdata", "records.golden.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	bodyChanged := bytes.Replace(records, []byte(`"body":"why"`), []byte(`"body":"why not"`), 1)
	assertBundleRejected(t, manifestForRecords(t, manifest, bodyChanged, ""), bodyChanged, "payload hash")

	unknownField := bytes.Replace(records, []byte(`"authority":`), []byte(`"added":1,"authority":`), 1)
	assertBundleRejected(t, manifestForRecords(t, manifest, unknownField, ""), unknownField, "unknown field")

	lines := bytes.Split(bytes.TrimSuffix(records, []byte("\n")), []byte("\n"))
	reversed := append(append(append([]byte(nil), lines[1]...), '\n'), lines[0]...)
	reversed = append(reversed, '\n')
	var second, first Envelope
	if err := strictDecode(lines[1], &second); err != nil {
		t.Fatal(err)
	}
	if err := strictDecode(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	reversedSet := hashHex([]byte(second.EnvelopeSHA256 + "\n" + first.EnvelopeSHA256 + "\n"))
	assertBundleRejected(t, manifestForRecords(t, manifest, reversed, reversedSet), reversed, "out of order")

	first.RecordSchemaVersion = 2
	first.EnvelopeSHA256 = ""
	withoutHash, err := canonicalMarshal(first)
	if err != nil {
		t.Fatal(err)
	}
	first.EnvelopeSHA256 = hashHex(withoutHash)
	changedFirst, err := canonicalMarshal(first)
	if err != nil {
		t.Fatal(err)
	}
	unknownSchema := append(append(append([]byte(nil), changedFirst...), '\n'), lines[1]...)
	unknownSchema = append(unknownSchema, '\n')
	unknownSet := hashHex([]byte(first.EnvelopeSHA256 + "\n" + second.EnvelopeSHA256 + "\n"))
	assertBundleRejected(t, manifestForRecords(t, manifest, unknownSchema, unknownSet), unknownSchema, "unsupported record schema")

	if _, err := ValidateBundle(append([]byte(" "), manifest...), records); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("accepted noncanonical manifest: %v", err)
	}
}

func manifestForRecords(t *testing.T, input, records []byte, recordSetHash string) []byte {
	t.Helper()
	var manifest Manifest
	if err := strictDecode(input, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.RecordsSHA256 = hashHex(records)
	if recordSetHash != "" {
		manifest.RecordSetSHA256 = recordSetHash
	}
	value, err := canonicalMarshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertBundleRejected(t *testing.T, manifest, records []byte, want string) {
	t.Helper()
	if _, err := ValidateBundle(manifest, records); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q rejection, got %v", want, err)
	}
}
