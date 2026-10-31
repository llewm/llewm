//go:build linux

package recordsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestCapturePublicationRejectsConcurrentDamage(t *testing.T) {
	ctx := context.Background()
	makeObject := func(id string) portable.EncodedEnvelope {
		t.Helper()
		e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	old, newObject := makeObject("old"), makeObject("new")
	for _, mode := range []string{"own-additions", "foreign-existing", "interrupted", "before-publication", "old-record", "old-provenance", "old-deletion", "new-record", "new-provenance", "foreign-provenance", "unexpected-file"} {
		t.Run(mode, func(t *testing.T) {
			a, err := OpenArchive(t.TempDir(), lineage)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "foreign-existing" {
				if _, err := a.Put(old, "peer"); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.CaptureLocal([]portable.EncodedEnvelope{old}); err != nil {
				t.Fatal(err)
			}
			_, snapshot, err := directoryChangeSnapshot(ctx, a.Directory, true)
			if err != nil {
				t.Fatal(err)
			}
			objects := []portable.EncodedEnvelope{old, newObject}
			if mode == "before-publication" {
				// A valid foreign arrival after the initial observation must
				// not become part of our own publication receipt.
				if _, err := a.Put(makeObject("foreign"), "peer"); err != nil {
					t.Fatal(err)
				}
			}
			p := prepareCapturePublication(snapshot, objects)
			if p == nil {
				t.Fatal("publication not prepared")
			}
			if mode != "interrupted" {
				if err := a.CaptureLocal(objects); err != nil {
					t.Fatal(err)
				}
			}
			rewrite := func(path string) {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				body[0] ^= 1
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "old-record":
				rewrite(filepath.Join(a.Directory, portable.MemoryRecordPath(old.Envelope)))
			case "old-provenance":
				rewrite(filepath.Join(a.Directory, "origins", old.Envelope.EnvelopeSHA256, Hash("local")))
			case "old-deletion":
				if err := os.Remove(filepath.Join(a.Directory, portable.MemoryRecordPath(old.Envelope))); err != nil {
					t.Fatal(err)
				}
			case "new-record":
				rewrite(filepath.Join(a.Directory, portable.MemoryRecordPath(newObject.Envelope)))
			case "new-provenance":
				rewrite(filepath.Join(a.Directory, "origins", newObject.Envelope.EnvelopeSHA256, Hash("local")))
			case "foreign-provenance":
				if _, err := a.Put(newObject, "peer"); err != nil {
					t.Fatal(err)
				}
			case "unexpected-file":
				if err := os.WriteFile(filepath.Join(a.Directory, "origins", "unexpected"), []byte("extra"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := p.completedStamp(ctx, a)
			if mode == "own-additions" || mode == "foreign-existing" {
				current, err := a.ChangeStamp(ctx)
				if err != nil || got == "" || got != current {
					t.Fatalf("own additions: %q %q %v", got, current, err)
				}
				if mode == "foreign-existing" {
					if _, err := os.Stat(filepath.Join(a.Directory, "origins", old.Envelope.EnvelopeSHA256, Hash("local"))); !os.IsNotExist(err) {
						t.Fatal("foreign provenance became local")
					}
				}
			} else if got != "" {
				t.Fatal("unexpected mutation blessed")
			}
		})
	}
}

func TestCapturePublicationReusesInitialMetadataSnapshot(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "snapshot")
	a, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	appendDecision := func(id string) {
		t.Helper()
		if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: id, Title: id, Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
			t.Fatal(err)
		}
	}
	appendDecision("old")
	for i := 0; i < 4; i++ {
		if _, err := a.CaptureProject(ctx, db, project); err != nil {
			t.Fatal(err)
		}
	}
	if _, snapshot, err := a.captureStart(ctx, db, project); err != nil || snapshot != nil {
		t.Fatalf("unchanged capture retained metadata: %d %v", len(snapshot), err)
	}
	_, before, err := directoryChangeSnapshot(ctx, a.Directory, true)
	if err != nil {
		t.Fatal(err)
	}
	appendDecision("new")
	measured, work := WithWorkMetrics(ctx)
	if reused, err := a.CaptureProject(measured, db, project); err != nil || reused {
		t.Fatalf("capture delta: %t %v", reused, err)
	}
	_, after, err := directoryChangeSnapshot(ctx, a.Directory, true)
	if err != nil {
		t.Fatal(err)
	}
	// Initial observation, completed publication, and final guard. The first
	// write no longer needs a fourth full metadata walk.
	if got, want := work.Counts()["archive_metadata_entries_visited"], int64(len(before)+2*len(after)); got != want {
		t.Fatalf("metadata visits: got %d want %d", got, want)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("publication not acknowledged: %t %v", reused, err)
	}
}

func TestCaptureLocalObservesOnlyActualCreation(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "observer")
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	export, err := portable.EncodeMemoryExport(ctx, db, project)
	if err != nil {
		t.Fatal(err)
	}
	a, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.ChangeStamp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := a.captureLocal(export.Envelopes, func() {
		if calls == 0 {
			stamp, err := a.ChangeStamp(ctx)
			if err != nil || stamp != before {
				t.Fatal("observer ran after filesystem mutation")
			}
		}
		calls++
	}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("missing creation observation")
	}
	if err := a.captureLocal(export.Envelopes, func() { t.Fatal("existing files triggered publication tracking") }); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureProjectAcknowledgesItsOwnPublication(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication")
	a, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (decision.Service{DB: db}).Append(ctx, decision.Record{ProjectID: project, ID: "decision", Title: "Decision", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	// Retain under the export transaction before capture, as an importer or
	// earlier export can do. The capture's database snapshot is now stable.
	if _, err := portable.EncodeMemoryExport(ctx, db, project); err != nil {
		t.Fatal(err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || reused {
		t.Fatalf("initial capture: %t %v", reused, err)
	}
	if reused, err := a.CaptureProject(ctx, db, project); err != nil || !reused {
		t.Fatalf("own publication not acknowledged: %t %v", reused, err)
	}
}
