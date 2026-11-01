package memsync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func observedMachine(t *testing.T, name string) machine {
	t.Helper()
	m := newMachine(t, name, bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "original", "Original history")
	warmUnchanged(t, m)
	return m
}

func TestObservedGitCycleReadsOnlyNewBlocksAndRepairsDamagedInventory(t *testing.T) {
	m := observedMachine(t, "observed-delta")
	measure := func() map[string]int64 {
		t.Helper()
		ctx, metrics := syncwork.WithWorkMetrics(context.Background())
		result, err := m.syncer.Sync(ctx)
		if err != nil || result.Outcome != OutcomeSynced || result.Unchanged {
			t.Fatalf("cycle: %+v %v", result, err)
		}
		return metrics.Counts()
	}
	m.decide(t, "new", "New observation")
	counts := measure()
	if counts["archive_audit_envelopes"] != 0 || counts["archive_journal_rows_read"] != 6 || counts["archive_envelopes_read"] != 4 || counts["fallback_publication_journal_full"] != 0 {
		t.Fatalf("known cycle scanned old archive: %v", counts)
	}
	if counts["semantic_journal_delta_cycles"] != 1 || counts["fallback_semantic_journal_full"] != 0 || counts["semantic_catalog_blocks_read"] != 0 || counts["dependency_membership_records"] != 1 || counts["operational_full_loads"] != 0 || counts["local_state_records_visited"] != 0 {
		t.Fatalf("known cycle replayed old semantic state: %v", counts)
	}
	if counts["fallback_git_observation_full"] != 0 || counts["git_observation_blocks"] != 1 || counts["git_observation_delta"] < 2 {
		t.Fatalf("old blocks reread: %v", counts)
	}
	if _, err := m.db.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, m.project); err != nil {
		t.Fatal(err)
	}
	m.decide(t, "after-damage", "After damage")
	counts = measure()
	if counts["fallback_git_observation_full"] == 0 || counts["git_observation_blocks"] < 3 {
		t.Fatalf("inventory damage reused observation: %v", counts)
	}
	var damaged int
	if err := m.db.QueryRow(`SELECT count(*) FROM history_index_envelopes WHERE project_id=? AND record_id='damaged'`, m.project).Scan(&damaged); err != nil || damaged != 0 {
		t.Fatalf("unrepaired cache: %d %v", damaged, err)
	}
}

func TestObservedGitMissingGenerationAndModifiedTriggerRequireFullValidation(t *testing.T) {
	m := observedMachine(t, "observed-tracking")
	if _, err := m.db.Exec(`DELETE FROM history_index_inventory_generation WHERE project_id=?`, m.project); err != nil {
		t.Fatal(err)
	}
	ctx, metrics := syncwork.WithWorkMetrics(context.Background())
	result, err := m.syncer.Sync(ctx)
	if err != nil || result.Outcome != OutcomeSynced || result.Unchanged || metrics.Counts()["fallback_git_observation_full"] == 0 {
		t.Fatalf("missing generation: %+v %v %v", result, err, metrics.Counts())
	}
	const name = "history_inventory_history_index_blocks_insert"
	var definition string
	if err := m.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`DROP TRIGGER ` + name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`CREATE TRIGGER ` + name + ` AFTER INSERT ON history_index_blocks BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	ctx, metrics = syncwork.WithWorkMetrics(context.Background())
	result, err = m.syncer.Sync(ctx)
	if err != nil || result.Outcome != OutcomeSynced || result.Unchanged || metrics.Counts()["fallback_git_observation_full"] < 2 {
		t.Fatalf("modified tracking: %+v %v %v", result, err, metrics.Counts())
	}
	if _, err := m.db.Exec(`DROP TRIGGER ` + name); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(definition); err != nil {
		t.Fatal(err)
	}
	warmUnchanged(t, m)
}

func TestObservedPublicationIntentSurvivesBeforeGitCommit(t *testing.T) {
	m := observedMachine(t, "observed-pending")
	ctx := context.Background()
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockFile(filepath.Join(m.syncer.Clone, "continuity-sync.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := m.syncer.observeGitPublications(ctx, repo, testLineage, false)
	if err != nil || receipt == nil {
		t.Fatalf("observation: %v", err)
	}
	oldTree := receipt.Tree
	m.decide(t, "pending", "Pending publication")
	if _, err := m.syncer.Archive.CaptureProject(ctx, m.db, m.project); err != nil {
		t.Fatal(err)
	}
	planned, _, err := m.syncer.publishBlocks(ctx, repo, testLineage, receipt)
	if err != nil || len(planned) == 0 {
		t.Fatalf("plan: %d %v", len(planned), err)
	}
	if receipt.Tree != oldTree {
		t.Fatal("intent claimed a Git commit before publication")
	}
	lock.Close()
	// A fresh cycle must deliver staged intent although its archive cursor and
	// inventory observation already include those exact local objects.
	result := m.sync(t, OutcomeSynced)
	if result.ExportedRecords != 1 {
		t.Fatalf("lost pending publication: %+v", result)
	}
	peer := newMachine(t, "observed-pending-peer", m.syncer.Remote)
	peer.sync(t, OutcomeSynced)
	if peer.decisions(t) != "original,pending" {
		t.Fatalf("peer history: %s", peer.decisions(t))
	}
	index := recordsync.Index{DB: m.db, ProjectID: m.project, Lineage: testLineage}
	if _, err := index.ObservationStamp(ctx, *m.syncer.Archive); err != nil {
		t.Fatal(err)
	}
}

func TestObservedOldGitBytesRemainCoveredByExplicitFullAudit(t *testing.T) {
	m := observedMachine(t, "observed-audit")
	ctx := context.Background()
	repo, err := m.syncer.repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := m.syncer.trackedBlocks(ctx, repo, testLineage)
	if err != nil || len(blocks) == 0 {
		t.Fatalf("fixture blocks: %v", err)
	}
	directory, err := gitBlockDirectory(testLineage)
	if err != nil {
		t.Fatal(err)
	}
	oid, err := repo.run(ctx, "rev-parse", "HEAD:"+filepath.ToSlash(filepath.Join(directory, blocks[0].Hash+".block")))
	if err != nil {
		t.Fatal(err)
	}
	fixtureCorruptObject(t, repo, oid, "blob", []byte("corrupt previously observed blob"))
	measured, metrics := syncwork.WithWorkMetrics(ctx)
	arrivals, receipt, err := m.syncer.observeGitPublications(measured, repo, testLineage, false)
	if err != nil || receipt == nil || len(arrivals) != 0 || metrics.Counts()["block_reads"] != 0 {
		t.Fatalf("old bytes unexpectedly reread: %d %v %v", len(arrivals), err, metrics.Counts())
	}
	if _, err := AuditStorage(ctx, m.syncer.Clone, m.syncer.Remote, testLineage, Access{}); err == nil {
		t.Fatal("explicit full audit hid old object corruption")
	}
}
