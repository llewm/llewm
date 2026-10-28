package recordsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

func TestPublicationMembershipAndRecovery(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	makeObject := func(id string) portable.EncodedEnvelope {
		t.Helper()
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: id, RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return object
	}
	local, next, foreign := makeObject("local"), makeObject("next"), makeObject("foreign")
	for _, item := range []struct {
		object portable.EncodedEnvelope
		origin string
	}{{local, "local"}, {foreign, "folder:source"}} {
		if _, err := archive.Put(item.object, item.origin); err != nil {
			t.Fatal(err)
		}
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	endpoint := "git:destination"
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_publication BEFORE INSERT ON history_index_publications BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if planned, inventory, err := index.PlanPublicationWithInventory(ctx, archive, endpoint, false, nil); err == nil || planned != nil || inventory != nil {
		t.Fatalf("publication failure exposed a partial snapshot: %+v %+v %v", planned, inventory, err)
	}
	var staged int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_blocks WHERE project_id=?`, project).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("partial publication inventory survived rollback: %d %v", staged, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_publication`); err != nil {
		t.Fatal(err)
	}
	initial, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(initial) != 1 || len(initial[0].Objects) != 1 || initial[0].Objects[0].Envelope.RecordID != "local" {
		t.Fatalf("initial plan: %+v %v", initial, err)
	}
	planned, inventory, err := index.PlanPublicationWithInventory(ctx, archive, endpoint, false, nil)
	if err != nil || len(planned) != 1 || planned[0].Hash != initial[0].Hash || len(inventory) != 1 || len(inventory[0].Objects) != 2 {
		t.Fatalf("restricted plan and complete local inventory: %+v %+v %v", planned, inventory, err)
	}
	for _, object := range planned[0].Objects {
		if object.Envelope.RecordID == "foreign" {
			t.Fatal("local inventory leaked foreign record into restricted publication")
		}
	}
	// No delivery yet: a fresh plan must return the exact staged block.
	retry, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(retry) != 1 || retry[0].Hash != initial[0].Hash {
		t.Fatalf("interrupted publication lost intent: %+v %v", retry, err)
	}
	if _, err := archive.Put(next, "local"); err != nil {
		t.Fatal(err)
	}
	updated, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(updated) != 2 {
		t.Fatalf("new record was not packed separately: %+v %v", updated, err)
	}
	oldFound := false
	for _, block := range updated {
		oldFound = oldFound || block.Hash == initial[0].Hash
	}
	if !oldFound {
		t.Fatal("adding a record repacked previously staged history")
	}
	withRelay, err := index.PlanPublication(ctx, archive, endpoint, true)
	if err != nil || len(withRelay) != 3 {
		t.Fatalf("relay did not add foreign record: %+v %v", withRelay, err)
	}
	withoutRelay, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(withoutRelay) != 2 {
		t.Fatalf("relay removal reused unauthorized block: %+v %v", withoutRelay, err)
	}
	for _, block := range withoutRelay {
		for _, object := range block.Objects {
			if object.Envelope.RecordID == "foreign" {
				t.Fatal("unauthorized relay record in publication plan")
			}
		}
	}
	// A block staged while relay was enabled may mix local and foreign
	// records. Disabling relay must repack only the still-allowed subset.
	mixed, err := index.PlanPublication(ctx, archive, "git:second", true)
	if err != nil || len(mixed) != 1 || len(mixed[0].Objects) != 3 {
		t.Fatalf("mixed block: %+v %v", mixed, err)
	}
	restricted, err := index.PlanPublication(ctx, archive, "git:second", false)
	if err != nil || len(restricted) != 1 || len(restricted[0].Objects) != 2 {
		t.Fatalf("restricted block: %+v %v", restricted, err)
	}
	for _, object := range restricted[0].Objects {
		if object.Envelope.RecordID == "foreign" {
			t.Fatal("mixed block leaked an unauthorized record")
		}
	}
	// Persisted intent must repair a destination file removed after publication.
	directory := t.TempDir()
	root, err := OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, block := range withoutRelay {
		if _, err := PublishBlock(root, lineage, block); err != nil {
			t.Fatal(err)
		}
	}
	path, err := blockPath(lineage, initial[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, path)); err != nil {
		t.Fatal(err)
	}
	retry, err = index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range retry {
		if _, err := PublishBlock(root, lineage, block); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := ScanBlocks(ctx, root, lineage)
	if err != nil || len(scan.Blocks) != 2 || scan.Pending != 0 || scan.Invalid != 0 {
		t.Fatalf("publication did not restore destination: %+v %v", scan, err)
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	for _, block := range scan.Blocks {
		if _, err := index.ObservePublication(ctx, archive, endpoint, block); err != nil {
			t.Fatal(err)
		}
	}
	rebuilt, err := index.PlanPublication(ctx, archive, endpoint, false)
	if err != nil || len(rebuilt) != 2 {
		t.Fatalf("destination scan did not rebuild membership: %+v %v", rebuilt, err)
	}
}

func TestConcurrentPublicationPlansDoNotDuplicateMembership(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-concurrent")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(fixture(t), "local"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	results := make(chan []Block, 4)
	failures := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			blocks, err := index.PlanPublication(context.Background(), archive, "folder:destination", false)
			results <- blocks
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected string
	for blocks := range results {
		if len(blocks) != 1 {
			t.Fatalf("unexpected plan: %+v", blocks)
		}
		if expected != "" && expected != blocks[0].Hash {
			t.Fatal("concurrent plans staged different blocks")
		}
		expected = blocks[0].Hash
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=?`, project).Scan(&count); err != nil || count != 1 {
		t.Fatalf("publication membership count %d: %v", count, err)
	}
}

func TestPublicationScansWithoutWriterLockAndRejectsStaleSnapshot(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-scan-contention")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	first := fixture(t)
	if _, err := archive.Put(first, "local"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	ready, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := index.planPublication(ctx, archive, "folder:target", false, nil, func() ([]portable.EncodedEnvelope, []portable.EncodedEnvelope, error) {
			allowed, all, err := archive.objects(ctx, "folder:target", false)
			close(ready)
			<-release
			return allowed, all, err
		})
		done <- err
	}()
	<-ready
	// Another plan can commit while the first is scanning, and must cause the
	// older snapshot to be rejected rather than stage a conflicting block.
	second, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "later", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err := archive.Put(second, "local"); err != nil {
		close(release)
		t.Fatal(err)
	}
	planCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err = index.PlanPublication(planCtx, archive, "folder:target", false)
	cancel()
	close(release)
	if err != nil {
		t.Fatalf("publication blocked behind archive scan: %v", err)
	}
	if err := <-done; !errors.Is(err, errPublicationChanged) {
		t.Fatalf("stale snapshot accepted: %v", err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:target", false); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestPublicationRejectsCorruptInventory(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "publication-corrupt")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	blocks, err := index.PlanPublication(ctx, archive, "folder:destination", false)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("initial plan: %+v %v", blocks, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(archive.Directory, portable.MemoryRecordPath(object.Envelope))); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:destination", false); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("corrupt inventory was not rejected: %v", err)
	}
	if err := index.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ObservePublication(ctx, archive, "folder:destination", blocks[0]); err != nil {
		t.Fatal(err)
	}
	if restored, err := index.PlanPublication(ctx, archive, "folder:destination", false); err != nil || len(restored) != 1 || restored[0].Hash != blocks[0].Hash {
		t.Fatalf("rebuild did not restore publication: %+v %v", restored, err)
	}
}

func TestPublicationRepairIgnoresCorruptOtherEndpoint(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "repair-destination")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	first := fixture(t)
	second, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "other-repair", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Put(first, "local"); err != nil {
		t.Fatal(err)
	}
	a, err := EncodeBlock(lineage, []portable.EncodedEnvelope{first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeBlock(lineage, []portable.EncodedEnvelope{second})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {a}, "folder:b": {b}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=?`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=? AND block_hash=?`, project, b.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:a", false); err != nil {
		t.Fatalf("unrelated corruption blocked repair: %v", err)
	}
	var version, memberships int
	if err := db.QueryRowContext(ctx, `SELECT index_version FROM history_index_blocks WHERE project_id=? AND block_hash=?`, project, b.Hash).Scan(&version); err != nil || version != 0 {
		t.Fatalf("unrelated block changed: %d %v", version, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=?`, project, b.Hash).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatalf("membership lost: %d %v", memberships, err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:b", false); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("corrupt destination accepted: %v", err)
	}
}

func TestStaleOtherEndpointDoesNotBlockPublication(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "stale-endpoint")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	if _, err := archive.Put(object, "local"); err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	blocks, err := index.PlanPublication(ctx, archive, "folder:a", false)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("initial plan: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=? AND block_hash=?`, project, blocks[0].Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:a", false); err != nil {
		t.Fatalf("reconstructable stale destination did not recover: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_blocks SET index_version=0 WHERE project_id=? AND block_hash=?`, project, blocks[0].Hash); err != nil {
		t.Fatal(err)
	}
	// A different destination containing different authorized records can
	// progress, while the stale endpoint remains explicitly recoverable.
	otherArchive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	other, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "other", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherArchive.Put(other, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, otherArchive, "folder:b", false); err != nil {
		t.Fatalf("unrelated endpoint blocked: %v", err)
	}
	if err := index.RepairBlock(ctx, blocks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:a", false); err != nil {
		t.Fatalf("repaired endpoint still blocked: %v", err)
	}
}

func TestPublicationRepairsDamagedStagedBlockFromArchive(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "archive-repair")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	first := fixture(t)
	second, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "archive-second", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []portable.EncodedEnvelope{first, second} {
		if _, err := archive.Put(object, "local"); err != nil {
			t.Fatal(err)
		}
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{first, second})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {block}, "git:b": {block}}); err != nil {
		t.Fatal(err)
	}
	damage := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=? AND block_hash=? AND envelope_hash=?`, project, block.Hash, first.Envelope.EnvelopeSHA256); err != nil {
			t.Fatal(err)
		}
	}
	damage()
	planned, err := index.PlanPublication(ctx, archive, "folder:a", false)
	if err != nil || len(planned) != 1 || planned[0].Hash != block.Hash {
		t.Fatalf("archive repair failed: %#v %v", planned, err)
	}
	var memberships int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_index_publications WHERE project_id=? AND block_hash=?`, project, block.Hash).Scan(&memberships); err != nil || memberships != 2 {
		t.Fatalf("repair lost endpoint membership: %d %v", memberships, err)
	}
	damage()
	if err := os.Remove(filepath.Join(archive.Directory, portable.MemoryRecordPath(first.Envelope))); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PlanPublication(ctx, archive, "folder:a", false); !errors.Is(err, ErrIndexRebuild) {
		t.Fatalf("missing archive object was accepted: %v", err)
	}
	var damaged string
	if err := db.QueryRowContext(ctx, `SELECT envelope_json FROM history_index_records WHERE project_id=? AND block_hash=? AND envelope_hash=?`, project, block.Hash, first.Envelope.EnvelopeSHA256).Scan(&damaged); err != nil || damaged != "{}" {
		t.Fatalf("failed repair changed inventory: %q %v", damaged, err)
	}
}

func TestArchiveRepairDoesNotAuthorizeRelay(t *testing.T) {
	ctx, db, project := indexedTestProject(t, "archive-repair-authorization")
	archive, err := OpenArchive(t.TempDir(), lineage)
	if err != nil {
		t.Fatal(err)
	}
	object := fixture(t)
	if _, err := archive.Put(object, "folder:b"); err != nil {
		t.Fatal(err)
	}
	block, err := EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	if err := index.Rebuild(ctx, map[string][]Block{"folder:a": {block}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE history_index_records SET envelope_json='{}' WHERE project_id=? AND block_hash=?`, project, block.Hash); err != nil {
		t.Fatal(err)
	}
	planned, err := index.PlanPublication(ctx, archive, "folder:a", false)
	if err != nil || len(planned) != 0 {
		t.Fatalf("repair authorized relay: %#v %v", planned, err)
	}
	if _, err := index.blockObjects(ctx, []Block{block}); err != nil {
		t.Fatalf("archive-backed block was not repaired: %v", err)
	}
}

// This measures publication discovery only. The current prototype still scans
// the per-record archive, so it must not be presented as an end-to-end speedup.
func BenchmarkPublicationDiscovery(b *testing.B) {
	for _, size := range []int{500, 5669} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			ctx, db, project := indexedTestProject(b, "publication-benchmark")
			archive, err := OpenArchive(b.TempDir(), lineage)
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < size; i++ {
				object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: fmt.Sprintf("decision-%06d", i), RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Authority: "caller_asserted", Payload: []byte(`{"value":"representative observation"}`)})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := archive.Put(object, "local"); err != nil {
					b.Fatal(err)
				}
			}
			index := Index{DB: db, ProjectID: project, Lineage: lineage}
			if _, err := index.PlanPublication(ctx, archive, "folder:benchmark", false); err != nil {
				b.Fatal(err)
			}
			b.Run("records", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := archive.Objects(ctx, "folder:benchmark", false); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("blocks", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := index.PlanPublication(ctx, archive, "folder:benchmark", false); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
