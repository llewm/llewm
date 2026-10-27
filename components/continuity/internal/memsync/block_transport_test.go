package memsync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func TestBlockTransportUsesOnlyTrackedValidatedPaths(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "tracked-blocks", bareRemote(t))
	m.decide(t, "tracked", "Tracked history")
	export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
	if err != nil || len(export.Envelopes) == 0 {
		t.Fatalf("export: %v", err)
	}
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(m.syncer.Clone)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	block, err := recordsync.EncodeBlock(testLineage, export.Envelopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordsync.PublishBlock(root, testLineage, block); err != nil {
		t.Fatal(err)
	}
	if got, err := m.syncer.trackedBlocks(ctx, repo, testLineage); err != nil || len(got) != 0 {
		t.Fatalf("untracked block included: %d %v", len(got), err)
	}
	if _, err := m.syncer.trackedBlockObservations(ctx, repo, testLineage, export.Envelopes[0].Envelope.EnvelopeSHA256); err == nil {
		t.Fatal("untracked observation accepted")
	}
	directory, err := recordsync.BlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "add", "--", directory); err != nil {
		t.Fatal(err)
	}
	if got, err := m.syncer.trackedBlocks(ctx, repo, testLineage); err != nil || len(got) != 1 || got[0].Hash != block.Hash {
		t.Fatalf("tracked block missing: %d %v", len(got), err)
	}
	hash := export.Envelopes[0].Envelope.EnvelopeSHA256
	if got, err := m.syncer.trackedBlockObservations(ctx, repo, testLineage, hash); err != nil || !bytes.Equal(got[hash], export.Envelopes[0].Bytes) {
		t.Fatalf("tracked observation mismatch: %v", err)
	}
	path := filepath.Join(m.syncer.Clone, directory, block.Hash+".block")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.syncer.trackedBlocks(ctx, repo, testLineage); err == nil {
		t.Fatal("accepted corrupt tracked block")
	}
}

func TestTrackedV3ConversionPreservesAllObservations(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "convert-blocks", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	makeObject := func(value string) portable.EncodedEnvelope {
		t.Helper()
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: testLineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "competing", RecordRevision: 1, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(value)})
		if err != nil {
			t.Fatal(err)
		}
		return object
	}
	objects := []portable.EncodedEnvelope{makeObject(`{"head":1}`), makeObject(`{"head":2}`)}
	if _, err := writeNew(m.syncer.Clone, objects); err != nil {
		t.Fatal(err)
	}
	if err := commit(ctx, repo, portable.MemoryLineageDir(testLineage), len(objects), "fixture: v3 history"); err != nil {
		t.Fatal(err)
	}
	identity, err := (portable.Service{DB: m.db}).Get(ctx, m.project)
	if err != nil || identity == nil {
		t.Fatalf("identity: %v", err)
	}
	if count, err := m.syncer.convertTrackedV3(ctx, repo, *identity); err != nil || count != len(objects) {
		t.Fatalf("conversion: %d %v", count, err)
	}
	if snapshot, err := m.syncer.trackedV3Snapshot(ctx, repo, testLineage); err != nil || len(snapshot.Objects) != 0 {
		t.Fatalf("v3 paths remain tracked: %+v %v", snapshot, err)
	}
	blocks, err := m.syncer.trackedBlocks(ctx, repo, testLineage)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, object := range objects {
		want[object.Envelope.EnvelopeSHA256] = true
	}
	for _, block := range blocks {
		for _, object := range block.Objects {
			delete(want, object.Envelope.EnvelopeSHA256)
		}
	}
	if len(want) != 0 {
		t.Fatalf("conversion lost observations: %v", want)
	}
	archived, err := archive.Objects(ctx, m.syncer.Endpoint(), false)
	if err != nil || len(archived) != len(objects) {
		t.Fatalf("conversion lost endpoint provenance: %d %v", len(archived), err)
	}
}

func TestTrackedV3ConversionRejectsCorruptionBeforeRemoval(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "corrupt-v3", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "record", "Observation")
	export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeNew(m.syncer.Clone, export.Envelopes); err != nil {
		t.Fatal(err)
	}
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := commit(ctx, repo, portable.MemoryLineageDir(testLineage), len(export.Envelopes), "fixture: v3 history"); err != nil {
		t.Fatal(err)
	}
	before, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.syncer.Clone, portable.MemoryRecordPath(export.Envelopes[0].Envelope))
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := (portable.Service{DB: m.db}).Get(ctx, m.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.syncer.convertTrackedV3(ctx, repo, *identity); err == nil {
		t.Fatal("corrupt observation accepted")
	}
	after, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil || before != after {
		t.Fatalf("conversion changed history: %s %s %v", before, after, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("conversion removed original: %v", err)
	}
}

func TestBlockTransportPublishesAndImportsThroughGit(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "block-source", remote), newMachine(t, "block-target", remote)
	aa, ba := archiveMachine(t, &a), archiveMachine(t, &b)
	a.syncer.Archive, b.syncer.Archive = &aa, &ba
	a.decide(t, "tracked", "Transported observation")
	export, err := portable.EncodeMemoryExport(ctx, a.db, a.project)
	if err != nil {
		t.Fatal(err)
	}
	if err := aa.CaptureLocal(export.Envelopes); err != nil {
		t.Fatal(err)
	}
	paths, count, err := a.syncer.publishBlocks(ctx, testLineage)
	if err != nil || count != len(export.Envelopes) {
		t.Fatalf("publish: %d %v", count, err)
	}
	repo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _ := recordsync.BlockDirectory(testLineage)
	untracked := filepath.Join(a.syncer.Clone, directory, "untracked.block")
	if err := os.WriteFile(untracked, []byte("not an observation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := commitBlocks(ctx, repo, paths, "fixture: publish blocks"); err != nil {
		t.Fatal(err)
	}
	if tracked, err := repo.run(ctx, "ls-files", "--", filepath.ToSlash(filepath.Join(directory, "untracked.block"))); err != nil || tracked != "" {
		t.Fatalf("untracked file was committed: %q %v", tracked, err)
	}
	if _, err := repo.run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	peer, err := b.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.run(ctx, "fetch", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.run(ctx, "checkout", "-B", "main", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if result, err := b.syncer.importTrackedBlocks(ctx, peer, testLineage); err != nil || result.ImportedRecords != len(export.Envelopes) {
		t.Fatalf("import: %+v %v", result, err)
	}
	if result, err := b.syncer.importTrackedBlocks(ctx, peer, testLineage); err != nil || result.ImportedRecords != 0 {
		t.Fatalf("replay: %+v %v", result, err)
	}
}

func TestBlockCycleConvergesAcrossGitPeers(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "block-cycle-a", remote), newMachine(t, "block-cycle-b", remote)
	aa, ba := archiveMachine(t, &a), archiveMachine(t, &b)
	a.syncer.Archive, b.syncer.Archive = &aa, &ba
	a.decide(t, "from-a", "Observation from A")
	if result, err := a.syncer.cycleBlocks(ctx, plan{push: true}); err != nil || result.Outcome != OutcomeSynced {
		t.Fatalf("A sync: %+v %v", result, err)
	}
	if result, err := b.syncer.cycleBlocks(ctx, plan{push: true}); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("B sync: %+v %v", result, err)
	}
	b.decide(t, "from-b", "Observation from B")
	if result, err := b.syncer.cycleBlocks(ctx, plan{push: true}); err != nil || result.Outcome != OutcomeSynced {
		t.Fatalf("B publication: %+v %v", result, err)
	}
	if result, err := a.syncer.cycleBlocks(ctx, plan{push: true}); err != nil || result.ImportedRecords != 1 {
		t.Fatalf("A import: %+v %v", result, err)
	}
}

func TestBlockCycleConvertsRemoteV3AndPreservesFork(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "v3-remote", remote), newMachine(t, "v4-converter", remote)
	a.topic(t, "transport", "Remote alternative")
	export, err := portable.EncodeMemoryExport(ctx, a.db, a.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeNew(a.syncer.Clone, export.Envelopes); err != nil {
		t.Fatal(err)
	}
	repo, err := a.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := commit(ctx, repo, portable.MemoryLineageDir(testLineage), len(export.Envelopes), "fixture: v3 history"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	b.topic(t, "transport", "Local alternative")
	archive := archiveMachine(t, &b)
	b.syncer.Archive = &archive
	result, err := b.syncer.cycleBlocks(ctx, plan{push: true, convert: true})
	if err != nil || !result.Converted || result.Forks != 1 {
		t.Fatalf("conversion: %+v %v", result, err)
	}
	if forks := b.forks(t); len(forks) != 1 || len(forks[0].Heads) != 2 {
		t.Fatalf("conversion lost competing observations: %+v", forks)
	}
	peer := newMachine(t, "v4-fresh", remote)
	pa := archiveMachine(t, &peer)
	peer.syncer.Archive = &pa
	if result, err := peer.syncer.cycleBlocks(ctx, plan{push: true}); err != nil || result.Outcome != OutcomeSynced {
		t.Fatalf("fresh peer: %+v %v", result, err)
	}
	if forks := peer.forks(t); len(forks) != 1 || len(forks[0].Heads) != 2 {
		t.Fatalf("fresh peer lost competing observations: %+v", forks)
	}
}
