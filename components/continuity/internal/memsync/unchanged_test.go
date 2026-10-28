//go:build linux

package memsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func warmUnchanged(t *testing.T, m machine) {
	t.Helper()
	for i := 0; i < 6; i++ {
		if m.sync(t, OutcomeSynced).Unchanged {
			return
		}
	}
	t.Fatal("steady state never used completion proof")
}

func TestUnchangedSyncInvalidationAndRecovery(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "unchanged-a", remote), newMachine(t, "unchanged-b", remote)
	a.decide(t, "first", "First observation")
	warmUnchanged(t, a)
	// Bookkeeping writes made by Sync must not dirty the next cycle.
	if !a.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("sync log invalidated proof")
	}
	// Another lineage's commit changes HEAD without changing our subtree.
	b.sync(t, OutcomeSynced)
	otherRepo, err := b.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.syncer.Clone, "unrelated"), []byte("Other lineage metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", "unrelated"}, {"commit", "--quiet", "-m", "Unrelated fixture change"}, {"push", "--quiet", "origin", "HEAD"}} {
		if _, err := otherRepo.run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	if !a.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("unrelated tree change forced full processing")
	}
	// An unrelated local commit still needs delivery even if our tree matches.
	localRepo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.syncer.Clone, "unpublished"), []byte("Pending delivery"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", "unpublished"}, {"commit", "--quiet", "-m", "Pending fixture delivery"}} {
		if _, err := localRepo.run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	if a.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("pending push skipped")
	}
	warmUnchanged(t, a)
	guard, err := a.syncer.guard(ctx, localRepo)
	if err != nil {
		t.Fatal(err)
	}
	a.decide(t, "local", "Local addition")
	if _, used, _ := a.syncer.tryUnchanged(ctx, localRepo, plan{push: true}, guard); used {
		t.Fatal("write after initial guard skipped")
	}
	if got := a.sync(t, OutcomeSynced); got.Unchanged || got.ExportedRecords == 0 {
		t.Fatalf("local change skipped: %+v", got)
	}
	warmUnchanged(t, a)
	b.decide(t, "remote", "Remote addition")
	b.sync(t, OutcomeSynced)
	if got := a.sync(t, OutcomeSynced); got.Unchanged || got.ImportedRecords == 0 {
		t.Fatalf("remote change skipped: %+v", got)
	}
	warmUnchanged(t, a)
	// Derived inventory corruption invalidates completion and is repaired.
	if _, err := a.db.Exec(`UPDATE history_index_blocks SET index_version=0`); err != nil {
		t.Fatal(err)
	}
	if got := a.sync(t, OutcomeSynced); got.Unchanged {
		t.Fatal("stale inventory skipped")
	}
	warmUnchanged(t, a)
	// Lost disposable proofs must simply fall back.
	if _, err := a.db.Exec(`DELETE FROM history_sync_proofs`); err != nil {
		t.Fatal(err)
	}
	if a.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("missing proof accepted")
	}
	warmUnchanged(t, a)
	// Uncommitted working-tree corruption cannot inherit a commit-tree proof.
	repo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := a.syncer.trackedBlocks(ctx, repo, testLineage)
	if err != nil || len(blocks) == 0 {
		t.Fatalf("blocks: %v", err)
	}
	dir, _ := recordsync.BlockDirectory(testLineage)
	path := filepath.Join(a.syncer.Clone, dir, blocks[0].Hash+".block")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := a.syncer.Sync(ctx); err == nil || got.Unchanged {
		t.Fatalf("dirty tree accepted: %+v %v", got, err)
	}
}

func TestUnchangedSyncDetectsArchiveEditsAndRouteChanges(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "archive-change", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "first", "Archive observation")
	warmUnchanged(t, m)
	m.syncer.Relay = true
	if m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("relay policy inherited proof")
	}
	warmUnchanged(t, m)
	export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(archive.Directory, portable.MemoryRecordPath(export.Envelopes[0].Envelope))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body := portable.MemoryFileBytes(export.Envelopes[0])
	body[len(body)-2] ^= 1
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	// Same size and restored mtime still changes ctime.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got, err := m.syncer.Sync(ctx); err == nil || got.Unchanged {
		t.Fatalf("archive corruption skipped: %+v %v", got, err)
	}
}

func TestUnchangedSyncIgnoresUnreferencedAttribution(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "raw-attribution", bareRemote(t))
	m.decide(t, "first", "Observation")
	warmUnchanged(t, m)
	at := time.Now().UTC()
	if _, err := (attribution.Service{DB: m.db}).AppendInvocation(ctx, attribution.Invocation{ID: "raw-tool", ProjectID: m.project, ActorType: "agent", IdentitySource: "caller_asserted", OccurredAt: at, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	if !m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("unreferenced attribution invalidated completion")
	}
	if _, err := (worknarrative.Service{DB: m.db}).Append(ctx, worknarrative.Record{ID: "linked", ProjectID: m.project, Title: "Linked", Trigger: "manual", Creator: "test", CreatorInvocationID: "raw-tool", Claims: []worknarrative.Claim{{Kind: "outcome", Text: "Observation", Authority: "caller_asserted"}}}); err != nil {
		t.Fatal(err)
	}
	if got := m.sync(t, OutcomeSynced); got.Unchanged || got.ExportedRecords < 2 {
		t.Fatalf("new attribution dependency skipped: %+v", got)
	}
	warmUnchanged(t, m)
	if _, err := m.db.Exec(`UPDATE actor_invocations SET model='changed' WHERE id='raw-tool'`); err != nil {
		t.Fatal(err)
	}
	if m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("referenced attribution edit skipped")
	}
}
