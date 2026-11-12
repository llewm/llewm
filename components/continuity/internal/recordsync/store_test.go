package recordsync

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/symlinktest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const lineage = "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestArchiveIsIndependentOfCallerBlockFields(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "archive-hints")
	archive, err := openTestArchive(t, db, project)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := PublishBlock(root, lineage, block); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	base, err := BlockDirectory(lineage)
	if err != nil {
		t.Fatal(err)
	}
	known, err := index.ReadBlock(ctx, root, filepath.Join(base, block.Hash+".block"))
	if err != nil {
		t.Fatal(err)
	}
	// Mutating exported fields cannot modify the private validation evidence.
	known.Objects[0].Bytes[0] = 'x'
	known.Objects[0].Envelope.RecordID = "forged"
	known.Objects[0].Envelope.Payload[0] = 'x'
	_, got, err := archive.objects(ctx, "folder:target", false)
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Bytes, object.Bytes) {
		t.Fatalf("valid cached result changed: %+v %v", got, err)
	}
	got[0].Bytes[0] = 'x'
	_, got, err = archive.objects(ctx, "folder:target", false)
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Bytes, object.Bytes) {
		t.Fatalf("cached result aliased caller data: %+v %v", got, err)
	}
	foreign, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "foreign", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(foreign, "folder:other"); err != nil {
		t.Fatal(err)
	}
	allowed, all, err := archive.objects(ctx, "folder:target", false)
	if err != nil || len(allowed) != 1 || len(all) != 2 || allowed[0].Envelope.EnvelopeSHA256 != object.Envelope.EnvelopeSHA256 {
		t.Fatalf("hint changed provenance or missed new data: %d %d %v", len(allowed), len(all), err)
	}
	for _, route := range []struct {
		endpoint string
		relay    bool
	}{{"folder:target", false}, {"folder:other", false}, {"folder:target", true}} {
		wantAllowed, wantAll, wantErr := archive.objects(ctx, route.endpoint, route.relay)
		gotAllowed, gotAll, gotErr := archive.objects(ctx, route.endpoint, route.relay)
		if wantErr != nil || gotErr != nil || !reflect.DeepEqual(wantAllowed, gotAllowed) || !reflect.DeepEqual(wantAll, gotAll) {
			t.Fatalf("cached scan differs for %+v: %v %v", route, wantErr, gotErr)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER history_archive_envelope_immutable_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE history_archive_envelopes SET envelope_json=cast('corrupt' as blob) WHERE project_id=? AND envelope_hash=?`, project, object.Envelope.EnvelopeSHA256); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archive.objects(ctx, "folder:target", false); err == nil {
		t.Fatal("cached hint accepted damaged archive bytes")
	}
	if _, _, err := archive.objects(ctx, "folder:target", false); err == nil {
		t.Fatal("forged unsealed hint accepted damaged archive bytes")
	}
}

func fixture(t *testing.T) portable.EncodedEnvelope {
	t.Helper()
	e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "sample", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestArchiveProvenanceAndConcurrentPublication(t *testing.T) {
	archive, err := standaloneTestArchive(t)
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
	var actual []byte
	err = archive.DB.QueryRow(`SELECT envelope_json FROM history_archive_envelopes WHERE project_id=? AND envelope_hash=?`, archive.ProjectID, e.Envelope.EnvelopeSHA256).Scan(&actual)
	if err != nil || !bytes.Equal(actual, e.Bytes) {
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
	symlinktest.Create(t, outside, filepath.Join(directory, "link"))
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
