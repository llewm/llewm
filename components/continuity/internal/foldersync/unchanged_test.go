//go:build linux

package foldersync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/decision"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func warmFolder(t *testing.T, s Syncer) {
	t.Helper()
	for i := 0; i < 7; i++ {
		result, err := s.Sync(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if result.Unchanged {
			if result.PeerReceipt != "unknown" {
				t.Fatal("local proof asserted peer receipt")
			}
			return
		}
	}
	t.Fatal("stable folder did not establish completion proof")
}

func TestFolderUnchangedProofInvalidatesAndPreservesIntegrity(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	marker, err := Create(directory)
	if err != nil {
		t.Fatal(err)
	}
	s := receiver(t, directory, marker.StoreID)
	if err := s.Archive.CaptureLocal(exportCheckpoint(t, s)); err != nil {
		t.Fatal(err)
	}
	if err := s.Convert(ctx); err != nil {
		t.Fatal(err)
	}
	warmFolder(t, s)
	var saved Result
	if err := s.Archive.Status(s.Endpoint.ID(), &saved); err != nil || !saved.Unchanged {
		t.Fatalf("mode not saved: %+v %v", saved, err)
	}
	// Local additions invalidate even when the provider directory is unchanged.
	if _, err := (decision.Service{DB: s.DB}).Append(ctx, decision.Record{ID: "addition", ProjectID: s.ProjectID, Title: "Addition", Rationale: "Observation", Disposition: "accepted", Actor: "test", ActorType: "human", Authority: "caller_asserted"}); err != nil {
		t.Fatal(err)
	}
	export, err := portable.EncodeMemoryExport(ctx, s.DB, s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Archive.CaptureLocal(export.Envelopes); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sync(ctx); err != nil || got.Unchanged || got.PublishedRecords == 0 {
		t.Fatalf("local addition skipped: %+v %v", got, err)
	}
	warmFolder(t, s)
	// Another writer publishes a new immutable block independently.
	peer := receiver(t, directory, marker.StoreID)
	block, err := recordsync.EncodeBlock(testLineage, exportCheckpoint(t, peer))
	if err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := recordsync.PublishBlock(root, testLineage, block); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sync(ctx); err != nil || got.Unchanged || got.ImportedRecords == 0 {
		t.Fatalf("external addition skipped: %+v %v", got, err)
	}
	warmFolder(t, s)
	// A missing destination block still takes the repair path.
	base, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, base, block.Hash+".block")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sync(ctx); err != nil || got.Unchanged || got.PublishedRecords == 0 {
		t.Fatalf("missing block not repaired: %+v %v", got, err)
	}
	warmFolder(t, s)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(nil), block.Bytes...)
	body[len(body)-2] ^= 1
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Sync(ctx); err == nil || got.Unchanged {
		t.Fatalf("corrupt block skipped: %+v %v", got, err)
	}
}
