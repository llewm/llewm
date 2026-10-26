package memsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/activity"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localHistoryMachine(t *testing.T, name string) machine {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	db, err := database.Initialize(ctx, filepath.Join(directory, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := (activity.Store{DB: db}).EnsureProject(ctx, filepath.Join(directory, name), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (portable.Service{DB: db}).Initialize(ctx, id, testLineage); err != nil {
		t.Fatal(err)
	}
	return machine{db: db, project: id}
}
func archiveMachine(t *testing.T, m *machine) recordsync.Archive {
	t.Helper()
	archive, err := recordsync.OpenArchive(filepath.Join(t.TempDir(), "archive"), testLineage)
	if err != nil {
		t.Fatal(err)
	}
	m.syncer.Archive = &archive
	return archive
}
func folderCycle(t *testing.T, m machine, archive recordsync.Archive, endpoint foldersync.Endpoint) foldersync.Result {
	t.Helper()
	ctx := context.Background()
	local, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.CaptureLocal(local.Envelopes); err != nil {
		t.Fatal(err)
	}
	result, err := (foldersync.Syncer{DB: m.db, ProjectID: m.project, Archive: archive, Endpoint: endpoint}).Sync(ctx)
	if err != nil {
		t.Fatalf("folder cycle: %+v %v", result, err)
	}
	return result
}

func TestHybridThreeMachinesConvergeWithExplicitDestinationRelays(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := localHistoryMachine(t, "folder-only")
	b, c := newMachine(t, "bridge", remote), newMachine(t, "git-only", remote)
	aa, ba, ca := archiveMachine(t, &a), archiveMachine(t, &b), archiveMachine(t, &c)
	shared := t.TempDir()
	marker, err := foldersync.Create(shared)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := foldersync.Endpoint{Name: "personal", Directory: shared, StoreID: marker.StoreID, LineageID: testLineage}
	a.decide(t, "from-folder", "Originated on folder-only machine")
	a.topic(t, "hybrid", "Base")
	folderCycle(t, a, aa, endpoint)
	folderCycle(t, b, ba, endpoint)
	if !strings.Contains(b.decisions(t), "from-folder") {
		t.Fatal("bridge did not import folder history")
	}
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	if strings.Contains(c.decisions(t), "from-folder") {
		t.Fatal("folder history relayed to Git without destination consent")
	}
	b.syncer.Relay = true
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	if !strings.Contains(c.decisions(t), "from-folder") {
		t.Fatal("authorized folder-to-Git relay lost history")
	}
	c.decide(t, "from-git", "Originated on Git-only machine")
	c.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	folderCycle(t, b, ba, endpoint)
	folderCycle(t, a, aa, endpoint)
	if strings.Contains(a.decisions(t), "from-git") {
		t.Fatal("Git history relayed to folder without destination consent")
	}
	endpoint.Relay = true
	folderCycle(t, b, ba, endpoint)
	folderCycle(t, a, aa, endpoint)
	if !strings.Contains(a.decisions(t), "from-git") {
		t.Fatal("authorized Git-to-folder relay lost history")
	}
	// Both disconnected ends advance the same revision. A bridge must preserve
	// both original envelopes, rather than exporting only its chosen local head.
	a.topic(t, "hybrid", "Folder offline revision")
	c.topic(t, "hybrid", "Git offline revision")
	c.sync(t, OutcomeSynced)
	b.sync(t, OutcomeSynced)
	folderCycle(t, b, ba, endpoint)
	folderCycle(t, a, aa, endpoint)
	folderCycle(t, b, ba, endpoint)
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	for _, m := range []machine{a, b, c} {
		forks := m.forks(t)
		if len(forks) != 1 || len(forks[0].Heads) != 2 {
			t.Fatalf("hybrid relay lost unresolved versions: %+v", forks)
		}
	}
	// Revisit all endpoints: object sets converge byte-for-byte and replay appends
	// no semantic decisions or new imports. No Git remote exists on machine A.
	folderCycle(t, a, aa, endpoint)
	folderCycle(t, b, ba, endpoint)
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	expected, err := aa.Objects(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, archive := range []recordsync.Archive{ba, ca} {
		actual, err := archive.Objects(ctx, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(actual) != len(expected) {
			t.Fatalf("archive object counts differ: %d/%d", len(actual), len(expected))
		}
		for i := range actual {
			if !bytes.Equal(actual[i].Bytes, expected[i].Bytes) {
				t.Fatalf("relay rewrote canonical envelope %d", i)
			}
		}
	}
	if a.syncer.Remote != "" || a.syncer.Clone != "" {
		t.Fatal("folder-only machine unexpectedly needs Git")
	}
	if result := folderCycle(t, a, aa, endpoint); result.ImportedRecords != 0 || result.PublishedRecords != 0 {
		t.Fatalf("replay was not idempotent: %+v", result)
	}
	// A resolution is itself portable history, and must settle the fork on both
	// transports without deleting either rejected historical observation.
	resolver := &app.App{DB: b.db, ProjectID: b.project}
	if _, err := resolver.ResolveRevisionFork(ctx, "topic_revision:hybrid@2", "", "owner", "user_reported"); err != nil {
		t.Fatal(err)
	}
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	folderCycle(t, b, ba, endpoint)
	folderCycle(t, a, aa, endpoint)
	for _, m := range []machine{a, b, c} {
		if len(m.forks(t)) != 0 {
			t.Fatal("resolution did not cross transport boundary")
		}
	}
	folderCycle(t, a, aa, endpoint)
	folderCycle(t, b, ba, endpoint)
	b.sync(t, OutcomeSynced)
	c.sync(t, OutcomeSynced)
	for _, m := range []machine{a, b, c} {
		if len(m.forks(t)) != 0 {
			t.Fatal("replaying full archive reopened settled fork")
		}
	}
	// Removing a shared object never deletes local history; an intact archive
	// restores it without changing the original record bytes.
	removed := filepath.Join(shared, portable.MemoryRecordPath(expected[0].Envelope))
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if result := folderCycle(t, a, aa, endpoint); result.PublishedRecords != 1 {
		t.Fatalf("retained record was not republished: %+v", result)
	}
}

// A new schema can remain opaque to a bridge while its current wire envelope
// integrity is still verifiable. It must never be silently dropped or applied.
func TestHybridRelaysOpaqueEnvelopeBytesWithoutSemanticApplication(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	producer, bridge := newMachine(t, "newer", remote), newMachine(t, "bridge", remote)
	consumer := localHistoryMachine(t, "folder-only")
	ba, ca := archiveMachine(t, &bridge), archiveMachine(t, &consumer)
	producer.decide(t, "known", "Known supported history")
	producer.sync(t, OutcomeSynced)
	e, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: testLineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "future", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	value := e.Envelope
	value.RecordSchemaVersion = 999
	value.EnvelopeSHA256 = ""
	body, _ := json.Marshal(value)
	body, err = portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	value.EnvelopeSHA256 = hex.EncodeToString(sum[:])
	body, _ = json.Marshal(value)
	body, err = portable.CanonicalizeJSON(body)
	if err != nil {
		t.Fatal(err)
	}
	future, err := portable.DecodeTransportEnvelope(body)
	if err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(producer.syncer.Clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordsync.WriteFile(root, portable.MemoryRecordPath(future.Envelope), portable.MemoryFileBytes(future), false); err != nil {
		t.Fatal(err)
	}
	root.Close()
	for _, args := range [][]string{{"add", "--all"}, {"-c", "user.name=fixture", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "Future schema fixture"}, {"push", "--quiet", "origin", "HEAD"}} {
		if output, err := exec.Command("git", append([]string{"-C", producer.syncer.Clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture commit: %v %s", err, output)
		}
	}
	result := bridge.sync(t, OutcomeSynced)
	if result.DeferredRecords != 1 {
		t.Fatalf("unsupported schema not deferred: %+v", result)
	}
	shared := t.TempDir()
	marker, err := foldersync.Create(shared)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := foldersync.Endpoint{Name: "shared", Directory: shared, StoreID: marker.StoreID, LineageID: testLineage, Relay: true}
	folderCycle(t, bridge, ba, endpoint)
	received := folderCycle(t, consumer, ca, endpoint)
	if received.DeferredRecords != 1 || received.Outcome != "pending" || consumer.decisions(t) != "known" {
		t.Fatalf("opaque schema applied or lost: %+v", received)
	}
	objects, err := ca.Objects(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, object := range objects {
		if object.Envelope.EnvelopeSHA256 == future.Envelope.EnvelopeSHA256 {
			found = true
			if !bytes.Equal(object.Bytes, future.Bytes) {
				t.Fatal("relay changed opaque bytes")
			}
		}
	}
	if !found {
		t.Fatal("opaque envelope disappeared between transports")
	}
}

func TestHybridRetainsAndRelaysHistoryBeforeMissingDependencyArrives(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	source := localHistoryMachine(t, "source")
	bridge, receiver := newMachine(t, "bridge", remote), newMachine(t, "receiver", remote)
	ba, ra := archiveMachine(t, &bridge), archiveMachine(t, &receiver)
	bridge.syncer.Relay = true
	_, err := (checkpoint.Service{DB: source.db}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: source.project, TaskID: "partial", Task: "Arrive out of order", NextAction: "Wait for dependency", Status: "in_progress"})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := portable.EncodeMemoryExport(ctx, source.db, source.project)
	if err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	marker, err := foldersync.Create(shared)
	if err != nil {
		t.Fatal(err)
	}
	root, err := recordsync.OpenRoot(shared)
	if err != nil {
		t.Fatal(err)
	}
	var action portable.EncodedEnvelope
	for _, e := range exported.Envelopes {
		if e.Envelope.RecordType == "checkpoint_action" {
			action = e
		}
	}
	if len(action.Bytes) == 0 {
		t.Fatal("missing action fixture")
	}
	if _, err := recordsync.WriteFile(root, portable.MemoryRecordPath(action.Envelope), portable.MemoryFileBytes(action), false); err != nil {
		t.Fatal(err)
	}
	endpoint := foldersync.Endpoint{Name: "partial", Directory: shared, StoreID: marker.StoreID, LineageID: testLineage}
	if result, err := (foldersync.Syncer{DB: bridge.db, ProjectID: bridge.project, Archive: ba, Endpoint: endpoint}).Sync(ctx); err == nil || result.Outcome != "pending" {
		t.Fatal("dependency was not pending")
	}
	for _, m := range []machine{bridge, receiver} {
		result, err := m.syncer.Sync(ctx)
		if err != nil || result.Outcome != OutcomeError || result.SemanticPending == "" || result.UnpushedCommits != 0 {
			t.Fatalf("pending history was not relayed durably: %+v %v", result, err)
		}
	}
	values, err := ra.Objects(ctx, "", true)
	if err != nil || len(values) != 1 || !bytes.Equal(values[0].Bytes, action.Bytes) {
		t.Fatal("receiver lost unapplied opaque transport observation")
	}
	for _, e := range exported.Envelopes {
		if _, err := recordsync.WriteFile(root, portable.MemoryRecordPath(e.Envelope), portable.MemoryFileBytes(e), false); err != nil {
			t.Fatal(err)
		}
	}
	root.Close()
	folderCycle(t, bridge, ba, endpoint)
	bridge.sync(t, OutcomeSynced)
	receiver.sync(t, OutcomeSynced)
	var count int
	if err := receiver.db.QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE project_id=?`, receiver.project).Scan(&count); err != nil || count != 1 {
		t.Fatalf("completed dependency set did not recover: %d %v", count, err)
	}
}
