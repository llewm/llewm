//go:build linux

package memsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/attribution"
	"github.com/llewm/llewm/components/continuity/internal/lease"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

func TestUnchangedSyncIgnoresCoordinationButTracksLeaseStamps(t *testing.T) {
	ctx := context.Background()
	m := newMachine(t, "lease-coordination", bareRemote(t))
	m.decide(t, "first", "Observation")
	warmUnchanged(t, m)
	service := lease.Service{DB: m.db}
	request := lease.Request{ProjectID: m.project, ScopeKind: "task", ScopeID: "working", Holder: "codex", SessionID: "before-restart", TTL: time.Hour}
	grant, err := service.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Token = grant.Lease.Token
	check := func() {
		t.Helper()
		if !m.sync(t, OutcomeSynced).Unchanged {
			t.Fatal("coordination forced full history processing")
		}
	}
	check()
	if _, err := service.Renew(ctx, request); err != nil {
		t.Fatal(err)
	}
	check()
	// Mirroring an already published grant only records remote delivery.
	if _, err := m.db.Exec(`INSERT INTO lease_remote_events(grant_id,sequence,version,observed_at) VALUES(?,1,'verified-remote-version',?)`, grant.Lease.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := service.Release(ctx, request); err != nil {
		t.Fatal(err)
	}
	check()
	// Unlike the grant itself, a stamp on a history write must travel.
	if err := lease.InsertStamp(ctx, m.db, m.project, lease.Stamp{RecordKind: lease.StampCheckpoint, RecordID: "recorded-write", ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, State: lease.Leased, Token: request.Token, Holder: request.Holder, SessionID: request.SessionID, InstanceID: grant.Lease.InstanceID, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got := m.sync(t, OutcomeSynced); got.Unchanged || got.ExportedRecords == 0 {
		t.Fatalf("canonical lease stamp skipped: %+v", got)
	}
}

func TestUnchangedSyncStillMirrorsRemoteLeaseChanges(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a, b := newMachine(t, "lease-mirror-a", remote), newMachine(t, "lease-mirror-b", remote)
	a.syncer.Leases = &lease.Service{DB: a.db}
	a.decide(t, "first", "Observation")
	warmUnchanged(t, a)
	peer := lease.Service{DB: b.db, Remote: b.syncer.LeaseRemote()}
	if _, err := peer.Acquire(ctx, lease.Request{ProjectID: b.project, ScopeKind: "task", ScopeID: "peer-work", Holder: "codex", SessionID: "peer-session", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := a.sync(t, OutcomeSynced); !got.Unchanged || len(got.LeaseNotes) != 0 {
		t.Fatalf("lease-only remote change forced history replay: %+v", got)
	}
	mirrored, err := a.syncer.Leases.List(ctx, a.project, false)
	if err != nil || len(mirrored) != 1 || mirrored[0].SessionID != "peer-session" || !mirrored[0].OnRemote {
		t.Fatalf("fast path missed lease change: %+v %v", mirrored, err)
	}
	if !a.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("cycle's own lease mirroring invalidated the next cycle")
	}
}

func warmUnchanged(t *testing.T, m machine) {
	t.Helper()
	for i := 0; i < 6; i++ {
		if m.sync(t, OutcomeSynced).Unchanged {
			return
		}
	}
	t.Fatal("steady state never used completion proof")
}

func TestUnchangedSyncIgnoresOtherProjectHistory(t *testing.T) {
	m := newMachine(t, "project-isolation", bareRemote(t))
	m.decide(t, "first", "Observation")
	warmUnchanged(t, m)
	if _, err := m.db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES('other','/other','Other','now')`); err != nil {
		t.Fatal(err)
	}
	if got := m.sync(t, OutcomeSynced); !got.Unchanged {
		t.Fatalf("unrelated project invalidated history: %+v", got)
	}
	if _, err := m.db.Exec(`UPDATE projects SET name='Updated' WHERE id='other'`); err != nil {
		t.Fatal(err)
	}
	if got := m.sync(t, OutcomeSynced); !got.Unchanged {
		t.Fatalf("unrelated revision invalidated history: %+v", got)
	}
	m.decide(t, "second", "Local observation")
	if got := m.sync(t, OutcomeSynced); got.Unchanged || got.ExportedRecords == 0 {
		t.Fatalf("local change was skipped: %+v", got)
	}
}

func TestUnchangedSyncIgnoresReplayProofRefresh(t *testing.T) {
	m := newMachine(t, "replay-proofs", bareRemote(t))
	m.decide(t, "first", "Observation")
	warmUnchanged(t, m)
	// All replay-cache mutations, including cascading membership removal,
	// leave transport inputs unchanged. They are checked on semantic reuse.
	for _, table := range []string{"history_index_valid_sets", "history_index_narrative_tip", "history_index_mixed_tip"} {
		for _, query := range []string{
			`INSERT INTO ` + table + `(project_id,set_hash,index_version) VALUES(?,'test-set',1) ON CONFLICT DO NOTHING`,
			`UPDATE ` + table + ` SET set_hash='changed-set',index_version=0 WHERE project_id=? AND set_hash='test-set'`,
			`DELETE FROM ` + table + ` WHERE project_id=?`,
		} {
			if _, err := m.db.Exec(query, m.project); err != nil {
				t.Fatal(err)
			}
			if !m.sync(t, OutcomeSynced).Unchanged {
				t.Fatalf("replay cache forced full processing: %s", query)
			}
		}
	}
	for _, kind := range []string{"narrative", "mixed"} {
		if _, err := m.db.Exec(`INSERT INTO history_index_`+kind+`_tip(project_id,set_hash,index_version) VALUES(?,'test-set',1)`, m.project); err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{
			`INSERT INTO history_index_` + kind + `_members(project_id,envelope_hash) VALUES(?,'test-envelope')`,
			`UPDATE history_index_` + kind + `_members SET envelope_hash='changed-envelope' WHERE project_id=?`,
			`DELETE FROM history_index_` + kind + `_members WHERE project_id=?`,
		} {
			if _, err := m.db.Exec(query, m.project); err != nil {
				t.Fatal(err)
			}
			if !m.sync(t, OutcomeSynced).Unchanged {
				t.Fatalf("replay membership forced full processing: %s", query)
			}
		}
	}
	// A missing completion proof forces a full replay. That replay refreshes
	// the discarded semantic caches and must be able to bless its own inputs.
	if _, err := m.db.Exec(`DELETE FROM history_sync_proofs`); err != nil {
		t.Fatal(err)
	}
	if m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("missing completion proof accepted")
	}
	if !m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("semantic proof refresh invalidated its own completion proof")
	}
	// Canonical writes and inventory damage still require full processing.
	m.decide(t, "second", "Another observation")
	if got := m.sync(t, OutcomeSynced); got.Unchanged || got.ExportedRecords == 0 {
		t.Fatalf("canonical addition skipped: %+v", got)
	}
	warmUnchanged(t, m)
	if _, err := m.db.Exec(`UPDATE history_index_records SET envelope_json='{}' WHERE project_id=?`, m.project); err != nil {
		t.Fatal(err)
	}
	if m.sync(t, OutcomeSynced).Unchanged {
		t.Fatal("damaged record inventory skipped")
	}
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
