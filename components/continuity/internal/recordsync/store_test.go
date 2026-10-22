package recordsync

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const lineage = "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixture(t *testing.T) portable.EncodedEnvelope {
	t.Helper()
	e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "sample", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestArchiveProvenanceAndConcurrentPublication(t *testing.T) {
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	e := fixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := archive.Put(e, "folder:source"); failures <- err }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.CaptureLocal([]portable.EncodedEnvelope{e}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		endpoint string
		relay    bool
		count    int
	}{{"folder:source", false, 1}, {"git:other", false, 0}, {"git:other", true, 1}} {
		records, err := archive.Objects(context.Background(), tc.endpoint, tc.relay)
		if err != nil || len(records) != tc.count {
			t.Fatalf("provenance permission bypass: %+v %d %v", tc, len(records), err)
		}
	}
	actual, err := os.ReadFile(filepath.Join(archive.Directory, portable.MemoryRecordPath(e.Envelope)))
	if err != nil || !bytes.Equal(actual, portable.MemoryFileBytes(e)) {
		t.Fatal("archive changed canonical bytes")
	}
}
func TestTransportPublicationRefusesSymlinksReplacementAndWrongPaths(t *testing.T) {
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := WriteFile(root, "immutable", []byte("original"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(root, "immutable", []byte("modified"), false); err == nil {
		t.Fatal("overwrote immutable object")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "link", 100); err == nil {
		t.Fatal("followed external symlink")
	}
	if _, err := WriteFile(root, "../escape", []byte("bad"), false); err == nil {
		t.Fatal("accepted path escape")
	}
	e := fixture(t)
	wrong := filepath.Join(portable.MemoryLineageDir(lineage), "decision", strings.Repeat("0", 64), e.Envelope.EnvelopeSHA256+".json")
	if _, err := WriteFile(root, wrong, portable.MemoryFileBytes(e), false); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadObject(root, wrong, lineage); err == nil {
		t.Fatal("accepted object at wrong hash path")
	}
	scan, err := Scan(context.Background(), root, lineage)
	if err != nil || scan.Pending != 1 || len(scan.Objects) != 0 {
		t.Fatalf("unsafe scan: %+v %v", scan, err)
	}
}
func TestOriginSurvivesInterruptedArchivePublication(t *testing.T) {
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	e := fixture(t)
	root, err := OpenRoot(archive.Directory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = WriteFile(root, filepath.Join("origins", e.Envelope.EnvelopeSHA256, Hash("folder:source")), []byte("observed\n"), false)
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.CaptureLocal([]portable.EncodedEnvelope{e}); err != nil {
		t.Fatal(err)
	}
	values, err := archive.Objects(context.Background(), "git:other", false)
	if err != nil || len(values) != 0 {
		t.Fatal("crash turned a received record into local origin")
	}
	values, err = archive.Objects(context.Background(), "folder:source", false)
	if err != nil || len(values) != 1 {
		t.Fatal("could not recover staged record from local semantic state")
	}
}
