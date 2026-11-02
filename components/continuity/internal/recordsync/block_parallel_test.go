package recordsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func parallelBlockLines(t testing.TB, n int) [][]byte {
	t.Helper()
	lines := make([][]byte, n)
	for i := range lines {
		lines[i] = archiveObject(t, fmt.Sprintf("parallel-%03d", i), fmt.Sprintf(`{"n":%d}`, i)).Bytes
	}
	sort.Slice(lines, func(i, j int) bool { return bytes.Compare(lines[i], lines[j]) < 0 })
	return lines
}
func parallelBlockBody(lines [][]byte) []byte {
	var b bytes.Buffer
	b.WriteString(blockHeader)
	for _, line := range lines {
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}
func futureParallelLine(t testing.TB) []byte {
	t.Helper()
	v := archiveObject(t, "future", `{}`).Envelope
	v.RecordSchemaVersion = 999
	v.EnvelopeSHA256 = ""
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	body, err = portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	v.EnvelopeSHA256 = Hash(string(body))
	body, err = json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	body, err = portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func compareParallelBlock(t testing.TB, expected string, body []byte, workers bool) {
	t.Helper()
	want, wantErr := referenceDecodeBlock(lineage, expected, body)
	got, err := DecodeBlock(lineage, expected, body)
	if (err == nil) != (wantErr == nil) {
		t.Fatalf("acceptance differs: %v vs%v", err, wantErr)
	}
	if err != nil {
		if err.Error() != wantErr.Error() {
			t.Fatalf("error order changed: %v vs%v", err, wantErr)
		}
	} else if !reflect.DeepEqual(got, want) {
		t.Fatal("typed block changed")
	}
	if !workers {
		return
	}
	lines := bytes.Split(body[len(blockHeader):len(body)-1], []byte{'\n'})
	for _, n := range []int{1, 2, 4, 20} {
		objects, err := decodeBlockEnvelopes(lineage, lines, n)
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("workers%d acceptance %v vs%v", n, err, wantErr)
		}
		if err != nil {
			if err.Error() != wantErr.Error() || objects != nil {
				t.Fatalf("workers%d partial output/error order: %v vs%v", n, err, wantErr)
			}
		} else if !reflect.DeepEqual(objects, want.Objects) {
			t.Fatalf("workers%d reordered objects", n)
		}
	}
}
func TestParallelBlockValidationMatchesSequentialReference(t *testing.T) {
	for _, n := range []int{1, 31, 32, 64, MaxBlockRecords} {
		t.Run(fmt.Sprintf("valid-%d", n), func(t *testing.T) {
			body := parallelBlockBody(parallelBlockLines(t, n))
			compareParallelBlock(t, Hash(string(body)), body, true)
		})
	}
	for _, mode := range []string{"envelope_before_order", "order_before_envelope", "lineage", "future"} {
		t.Run(mode, func(t *testing.T) {
			lines := parallelBlockLines(t, 64)
			switch mode {
			case "envelope_before_order":
				lines[2] = append(lines[2], ' ')
				lines[6] = lines[5]
			case "order_before_envelope":
				lines[2] = lines[1]
				lines[6] = append(lines[6], ' ')
			case "lineage":
				e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: "lineage:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RecordType: "decision", RecordSchemaVersion: 1, RecordID: "foreign", RecordRevision: 1, RecordedAt: time.Unix(1234567890, 0), Authority: "caller_asserted", Payload: []byte(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				lines[5] = e.Bytes
				sort.Slice(lines, func(i, j int) bool { return bytes.Compare(lines[i], lines[j]) < 0 })
			case "future":
				lines[5] = futureParallelLine(t)
				sort.Slice(lines, func(i, j int) bool { return bytes.Compare(lines[i], lines[j]) < 0 })
			}
			body := parallelBlockBody(lines)
			compareParallelBlock(t, "", body, true)
			if mode == "envelope_before_order" {
				if _, err := DecodeBlock(lineage, "", body); err == nil || !strings.HasPrefix(err.Error(), "invalid block envelope:") {
					t.Fatal("later sorting error won")
				}
			}
			if mode == "order_before_envelope" {
				if _, err := DecodeBlock(lineage, "", body); err == nil || err.Error() != "block envelopes are not strictly ordered" {
					t.Fatal("later envelope error won")
				}
			}
		})
	}
	body := parallelBlockBody(parallelBlockLines(t, 64))
	for _, bad := range [][]byte{body[:len(body)-1], append([]byte("wrong"), body...), parallelBlockBody(parallelBlockLines(t, MaxBlockRecords+1)), make([]byte, MaxBlockBytes+1)} {
		compareParallelBlock(t, "", bad, false)
	}
	compareParallelBlock(t, Hash("wrong"), body, false)
}
func FuzzParallelBlockMatchesSequentialReference(f *testing.F) {
	f.Add(parallelBlockBody(parallelBlockLines(f, 64)))
	f.Add([]byte(blockHeader))
	f.Add([]byte("invalid"))
	f.Fuzz(func(t *testing.T, body []byte) { compareParallelBlock(t, "", body, false) })
}
