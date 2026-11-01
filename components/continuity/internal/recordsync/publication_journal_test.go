package recordsync

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

type publicationJournalFixture struct {
	ctx       context.Context
	store     archiveStore
	archive   Archive
	index     Index
	scope     JournalPublicationScope
	committed map[string]bool
}

func journalFixture(t *testing.T) publicationJournalFixture {
	t.Helper()
	ctx, store := archiveStoreFixture(t)
	activateEmptyArchive(t, ctx, store)
	f := publicationJournalFixture{ctx: ctx, store: store, archive: Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}, index: Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}, committed: map[string]bool{}}
	f.scope = JournalPublicationScope{Endpoint: "git:fixture", Consumer: Hash("fixture publication consumer"), Tree: strings.Repeat("1", 40), HasBlock: func(_ context.Context, hash string) (bool, error) { return f.committed[hash], nil }, AppendOnly: func(context.Context, string) (bool, error) { return true, nil }}
	var err error
	f.scope.Observed, err = f.index.ObservationStamp(ctx, f.archive)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *publicationJournalFixture) plan(t *testing.T, wantSnapshot bool) JournalPublicationPlan {
	t.Helper()
	result, err := f.index.PlanJournalPublication(f.ctx, f.archive, f.scope, wantSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Observed != (ObservationStamp{}) {
		f.scope.Observed = result.Observed
	}
	return result
}
func (f *publicationJournalFixture) commit(t *testing.T, blocks []Block) {
	t.Helper()
	for _, block := range blocks {
		f.committed[block.Hash] = true
	}
	mutation, ok, err := f.index.ObservePublicationBatch(f.ctx, f.archive, f.scope.Endpoint, f.scope.Observed, false, blocks)
	if err != nil || !ok {
		t.Fatalf("observe committed: %v %v", ok, err)
	}
	f.scope.Tree = Hash(f.scope.Tree + "next")[:40]
	f.scope.Observed = mutation.After
	if err := f.index.AdvancePublicationObservation(f.ctx, f.archive, f.scope, mutation); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationJournalConsumesDeltasAndRetainsPendingIntent(t *testing.T) {
	f := journalFixture(t)
	original := []portable.EncodedEnvelope{archiveObject(t, "one", `{"value":1}`), archiveObject(t, "two", `{"value":2}`)}
	if err := f.archive.CaptureLocal(original); err != nil {
		t.Fatal(err)
	}
	first := f.plan(t, true)
	if first.Delta || len(first.Blocks) != 1 {
		t.Fatalf("initial: %+v", first)
	}
	originalHash := first.Snapshot[0].Hash
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err == nil {
		t.Fatal("pending intent granted completion proof")
	}
	f.commit(t, first.Blocks)
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err == nil {
		t.Fatal("unconsumed provenance granted completion proof")
	}
	settled := f.plan(t, true)
	if !settled.Delta || len(settled.Blocks) != 0 || settled.Snapshot[0].Hash != originalHash {
		t.Fatalf("settled: %+v", settled)
	}
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err != nil {
		t.Fatal(err)
	}
	if err := f.archive.CaptureLocal([]portable.EncodedEnvelope{archiveObject(t, "arrival", `{"value":3}`)}); err != nil {
		t.Fatal(err)
	}
	ctx, metrics := syncwork.WithWorkMetrics(f.ctx)
	f.ctx = ctx
	added := f.plan(t, false)
	counts := metrics.Counts()
	if !added.Delta || len(added.Blocks) != 1 || len(added.Blocks[0].Objects) != 1 || counts["archive_journal_rows_read"] != 2 || counts["archive_envelopes_read"] != 1 || counts["archive_audit_envelopes"] != 0 {
		t.Fatalf("delta: %+v work=%v", added, counts)
	}
	retried := f.plan(t, true)
	if !retried.Delta || len(retried.Blocks) != 1 || retried.Blocks[0].Hash != added.Blocks[0].Hash || len(retried.Snapshot) != 2 {
		t.Fatalf("pending retry: %+v", retried)
	}
	retained := false
	for _, block := range retried.Snapshot {
		retained = retained || block.Hash == originalHash
	}
	if !retained {
		t.Fatal("old semantic block was repacked")
	}
	f.commit(t, retried.Blocks)
	f.plan(t, true)
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationJournalRebuildDiscardsDamagedLocalSemanticCatalog(t *testing.T) {
	f := journalFixture(t)
	object := archiveObject(t, "foreign-only", `{"value":1}`)
	if _, err := f.archive.Put(object, "folder:foreign"); err != nil {
		t.Fatal(err)
	}
	initial := f.plan(t, true)
	if len(initial.Blocks) != 0 || len(initial.Snapshot) != 1 {
		t.Fatalf("foreign semantic snapshot: %+v", initial)
	}
	if _, err := f.store.DB.ExecContext(f.ctx, `UPDATE history_index_envelopes SET envelope_json='{}' WHERE project_id=?`, f.store.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := f.index.Rebuild(f.ctx, map[string][]Block{}); err != nil {
		t.Fatalf("disposable semantic catalog required a transport: %v", err)
	}
	rebuilt := f.plan(t, true)
	if rebuilt.Delta || len(rebuilt.Blocks) != 0 || len(rebuilt.Snapshot) != 1 || rebuilt.Snapshot[0].Hash != initial.Snapshot[0].Hash {
		t.Fatalf("archive did not recover exact foreign history: %+v", rebuilt)
	}
}

func TestPublicationJournalUnknownMutationsAndDamagedCursorsRequireFullSnapshot(t *testing.T) {
	f := journalFixture(t)
	object := archiveObject(t, "original", `{"value":1}`)
	if err := f.archive.CaptureLocal([]portable.EncodedEnvelope{object}); err != nil {
		t.Fatal(err)
	}
	first := f.plan(t, true)
	f.commit(t, first.Blocks)
	f.plan(t, true)
	if _, err := f.store.DB.Exec(`UPDATE history_index_envelopes SET record_id='damaged' WHERE project_id=?`, f.store.ProjectID); err != nil {
		t.Fatal(err)
	}
	// Recovery observation repairs the real committed blocks. Its Before token
	// is already unknown to publication, so handoff cannot bless that mutation.
	mutation, ok, err := f.index.ObservePublicationBatch(f.ctx, f.archive, f.scope.Endpoint, ObservationStamp{}, true, first.Blocks)
	if err != nil || !ok {
		t.Fatal(err)
	}
	f.scope.Observed = mutation.After
	if err := f.index.AdvancePublicationObservation(f.ctx, f.archive, f.scope, mutation); err != nil {
		t.Fatal(err)
	}
	recovered := f.plan(t, true)
	if recovered.Delta || len(recovered.Blocks) != 0 {
		t.Fatalf("unknown mutation reused projection: %+v", recovered)
	}
	if _, err := f.store.DB.Exec(`UPDATE history_archive_cursors SET checksum=? WHERE project_id=? AND consumer=?`, strings.Repeat("0", 64), f.store.ProjectID, publicationJournalCursor(f.scope.Consumer)); err != nil {
		t.Fatal(err)
	}
	if f.plan(t, true).Delta {
		t.Fatal("damaged cursor granted delta")
	}
	if err := f.archive.SaveStatus(publicationJournalKey(f.scope.Consumer), map[string]any{"version": 1, "unexpected": "data"}); err != nil {
		t.Fatal(err)
	}
	if f.plan(t, true).Delta {
		t.Fatal("damaged receipt granted delta")
	}
}

func TestPublicationJournalNeverAcknowledgesOversizedPendingSet(t *testing.T) {
	f := journalFixture(t)
	var objects []portable.EncodedEnvelope
	var staged []Block
	for i := 0; i < maxPublicationPending+1; i++ {
		object := archiveObject(t, fmt.Sprintf("pending-%04d", i), fmt.Sprintf(`{"value":%d}`, i))
		objects = append(objects, object)
		block, err := EncodeBlock(f.store.Lineage, []portable.EncodedEnvelope{object})
		if err != nil {
			t.Fatal(err)
		}
		staged = append(staged, block)
	}
	if err := f.archive.CaptureLocal(objects); err != nil {
		t.Fatal(err)
	}
	archiveTransaction(t, f.store, func(tx *sql.Tx) error {
		for _, block := range staged {
			if err := f.index.stageJournalBlockWithin(f.ctx, tx, Hash(f.scope.Endpoint), block); err != nil {
				return err
			}
		}
		return nil
	})
	var err error
	f.scope.Observed, err = f.index.ObservationStamp(f.ctx, f.archive)
	if err != nil {
		t.Fatal(err)
	}
	planned := f.plan(t, false)
	if len(planned.Blocks) != len(staged) {
		t.Fatalf("pending blocks lost: %d", len(planned.Blocks))
	}
	var cursors int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM history_archive_cursors WHERE project_id=? AND consumer=?`, f.store.ProjectID, publicationJournalCursor(f.scope.Consumer)).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("oversized intent was acknowledged: %d %v", cursors, err)
	}
	f.commit(t, planned.Blocks)
	recovered := f.plan(t, true)
	if recovered.Delta || len(recovered.Blocks) != 0 {
		t.Fatalf("large intent recovery: %+v", recovered)
	}
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationJournalPreservesForeignAuthorizationAndSemanticCoverage(t *testing.T) {
	f := journalFixture(t)
	f.plan(t, false)
	foreign := archiveObject(t, "foreign", `{"value":1}`)
	if _, err := f.archive.Put(foreign, "folder:other"); err != nil {
		t.Fatal(err)
	}
	retained := f.plan(t, true)
	if !retained.Delta || len(retained.Blocks) != 0 || len(retained.Snapshot) != 1 || len(retained.Snapshot[0].Objects) != 1 {
		t.Fatalf("foreign projection: %+v", retained)
	}
	var pseudoOrigins int
	if err := f.store.DB.QueryRow(`SELECT count(*) FROM history_archive_origins WHERE project_id=? AND origin_hash=?`, f.store.ProjectID, Hash("local-snapshot:"+f.store.Lineage)).Scan(&pseudoOrigins); err != nil || pseudoOrigins != 0 {
		t.Fatalf("semantic catalog granted transport provenance: %d %v", pseudoOrigins, err)
	}
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err != nil {
		t.Fatal(err)
	}
	// A policy change establishes its own complete materialization before any
	// previously excluded observation can become a publication intent.
	f.scope.Relay = true
	f.scope.Consumer = Hash("explicit relay approval")
	relayed := f.plan(t, true)
	if relayed.Delta || len(relayed.Blocks) != 1 || relayed.Blocks[0].Objects[0].Envelope.EnvelopeSHA256 != foreign.Envelope.EnvelopeSHA256 {
		t.Fatalf("explicit relay: %+v", relayed)
	}
	f.commit(t, relayed.Blocks)
	f.plan(t, true)
	if _, err := f.index.PublicationJournalStamp(f.ctx, f.archive, f.scope); err != nil {
		t.Fatal(err)
	}
}
