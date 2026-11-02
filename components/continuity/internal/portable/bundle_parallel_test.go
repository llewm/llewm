package portable

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parallelBundleFixture(t testing.TB, n int) Bundle {
	t.Helper()
	lineage := "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	at := time.Unix(1700000000, 123).UTC()
	specs := make([]EnvelopeSpec, n)
	for i := range specs {
		specs[i] = EnvelopeSpec{LineageID: lineage, RecordType: "work_item", RecordSchemaVersion: 1, RecordID: fmt.Sprintf("record-%06d", i), RecordedAt: at, Authority: "caller_asserted", Refs: []Reference{{Kind: "task", ID: "bundle"}}, Payload: []byte(`{"nested":{"😀":[true,null,123]},"title":"Record"}`)}
	}
	b, err := BuildBundle(BundleSpec{ExportID: "export:parallel", LineageID: lineage, SourceInstanceID: "instance:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CreatedAt: at, SourceDatabaseSchema: "095_history_radix_writer.sql", SelectionProfile: "curated-v1", RedactionProfile: DefaultRedactionProfile()}, specs)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func compareParallelBundle(t testing.TB, manifest, records []byte) {
	t.Helper()
	want, wantErr := referenceValidateBundle(manifest, records)
	got, err := ValidateBundle(manifest, records)
	if (err == nil) != (wantErr == nil) {
		t.Fatalf("acceptance differs: %v vs %v", err, wantErr)
	}
	if err != nil {
		if err.Error() != wantErr.Error() {
			t.Fatalf("error order changed: %v vs %v", err, wantErr)
		}
	} else if !reflect.DeepEqual(got, want) {
		t.Fatal("typed bundle changed")
	}
}
func changedParallelBundleLine(t testing.TB, line []byte, change func(*Envelope)) []byte {
	t.Helper()
	var e Envelope
	if err := strictDecode(line, &e); err != nil {
		t.Fatal(err)
	}
	change(&e)
	e.EnvelopeSHA256 = ""
	body, err := canonicalMarshal(e)
	if err != nil {
		t.Fatal(err)
	}
	e.EnvelopeSHA256 = hashHex(body)
	body, err = canonicalMarshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func TestParallelBundleValidationMatchesSequentialReference(t *testing.T) {
	for _, n := range []int{0, 1, 31, 32, 127, 128, 129, 257} {
		t.Run(fmt.Sprintf("valid-%d", n), func(t *testing.T) {
			b := parallelBundleFixture(t, n)
			compareParallelBundle(t, b.ManifestBytes, b.RecordsBytes)
			if n == 0 {
				return
			}
			lines := bytes.Split(bytes.TrimSuffix(b.RecordsBytes, []byte{'\n'}), []byte{'\n'})
			for _, workers := range []int{1, 2, 4, 20} {
				for first := 0; first < len(lines); first += 128 {
					end := min(first+128, len(lines))
					values, errs := validateBundleRecords(b.Manifest.LineageID, lines[first:end], first, workers)
					for j := range values {
						if errs[j] != nil || !reflect.DeepEqual(values[j], b.Records[first+j].Envelope) {
							t.Fatalf("workers%d changed line%d: %v", workers, first+j, errs[j])
						}
					}
				}
			}
		})
	}
	for _, mode := range []string{"canonical_before_order", "order_before_canonical", "boundary_order_first", "boundary_canonical_first", "empty", "unknown_field", "unknown_schema", "foreign_lineage", "payload_hash"} {
		t.Run(mode, func(t *testing.T) {
			b := parallelBundleFixture(t, 257)
			lines := bytes.Split(bytes.TrimSuffix(b.RecordsBytes, []byte{'\n'}), []byte{'\n'})
			switch mode {
			case "canonical_before_order":
				lines[2] = append(lines[2], ' ')
				lines[6] = lines[5]
			case "order_before_canonical":
				lines[2] = lines[1]
				lines[6] = append(lines[6], ' ')
			case "boundary_order_first":
				lines[127] = lines[126]
				lines[128] = append(lines[128], ' ')
			case "boundary_canonical_first":
				lines[127] = append(lines[127], ' ')
				lines[128] = lines[127]
			case "empty":
				lines[128] = nil
			case "unknown_field":
				lines[128] = bytes.Replace(lines[128], []byte(`"authority":`), []byte(`"added":1,"authority":`), 1)
			case "unknown_schema":
				lines[128] = changedParallelBundleLine(t, lines[128], func(e *Envelope) { e.RecordSchemaVersion = 999 })
			case "foreign_lineage":
				lines[128] = changedParallelBundleLine(t, lines[128], func(e *Envelope) { e.LineageID = "lineage:cccccccccccccccccccccccccccccccc" })
			case "payload_hash":
				lines[128] = bytes.Replace(lines[128], []byte(`"title":"Record"`), []byte(`"title":"Changed"`), 1)
			}
			for first := 0; first < len(lines); first += 128 {
				batch := lines[first:min(first+128, len(lines))]
				want, wantErrs := validateBundleRecords(b.Manifest.LineageID, batch, first, 1)
				for _, workers := range []int{2, 4, 20} {
					got, errs := validateBundleRecords(b.Manifest.LineageID, batch, first, workers)
					for i := range want {
						if (errs[i] == nil) != (wantErrs[i] == nil) {
							t.Fatalf("workers%d changed line%d acceptance", workers, first+i)
						}
						if wantErrs[i] != nil {
							if errs[i].Error() != wantErrs[i].Error() {
								t.Fatal("parallel line error changed")
							}
							break
						}
						if !reflect.DeepEqual(got[i], want[i]) {
							t.Fatal("parallel line value changed")
						}
					}
				}
			}
			records := append(bytes.Join(lines, []byte{'\n'}), '\n')
			manifest := manifestForRecords(t, b.ManifestBytes, records, "")
			compareParallelBundle(t, manifest, records)
			_, err := ValidateBundle(manifest, records)
			if err == nil {
				t.Fatal("invalid fixture accepted")
			}
			if mode == "canonical_before_order" && !strings.Contains(err.Error(), "record line 3 is not canonical") {
				t.Fatal(err)
			}
			if mode == "order_before_canonical" && !strings.Contains(err.Error(), "record line 3 is duplicate or out of order") {
				t.Fatal(err)
			}
			if mode == "boundary_order_first" && !strings.Contains(err.Error(), "record line 128 is duplicate or out of order") {
				t.Fatal(err)
			}
			if mode == "boundary_canonical_first" && !strings.Contains(err.Error(), "record line 128 is not canonical") {
				t.Fatal(err)
			}
		})
	}
	b := parallelBundleFixture(t, 32)
	compareParallelBundle(t, append([]byte{' '}, b.ManifestBytes...), b.RecordsBytes)
	compareParallelBundle(t, b.ManifestBytes, b.RecordsBytes[:len(b.RecordsBytes)-1])
	compareParallelBundle(t, b.ManifestBytes, []byte("wrong hash\n"))
	compareParallelBundle(t, make([]byte, MaxManifestBytes+1), nil)
	compareParallelBundle(t, b.ManifestBytes, make([]byte, MaxRecordsBytes+1))
	records := bytes.Repeat([]byte{'\n'}, MaxRecordCount+1)
	compareParallelBundle(t, manifestForRecords(t, b.ManifestBytes, records, ""), records)
}
func FuzzParallelBundleMatchesSequentialReference(f *testing.F) {
	b := parallelBundleFixture(f, 129)
	f.Add(b.RecordsBytes)
	f.Add([]byte("invalid\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, records []byte) {
		manifest := manifestForRecords(t, b.ManifestBytes, records, "")
		compareParallelBundle(t, manifest, records)
	})
}
