package memsync

import (
	"context"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
	"testing"
)

func TestHistorySyncWorkMetricsMeasureOnlySelectedCycle(t *testing.T) {
	m := newMachine(t, "measured-cycle", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "existing", "Existing observation")
	for range 2 {
		m.sync(t, OutcomeSynced)
	}
	m.decide(t, "arrival", "New observation")
	ctx, metrics := syncwork.WithWorkMetrics(context.Background())
	result, err := m.syncer.Sync(ctx)
	if err != nil || result.Outcome != OutcomeSynced || result.ExportedRecords != 1 || result.ImportedRecords != 0 {
		t.Fatalf("measured cycle changed behavior: %+v %v", result, err)
	}
	counts := metrics.Counts()
	for _, name := range []string{"operational_specs_loaded", "snapshot_envelope_encodes", "published_block_bytes", "block_read_bytes", "git_observation_blocks", "git_observation_delta", "archive_envelopes_inserted", "transport_tree_entries_read"} {
		if counts[name] <= 0 {
			t.Errorf("missing observed work %s: %v", name, counts)
		}
	}
	if counts["inventory_records_checked"] != 0 {
		t.Errorf("known Git observation repeated inventory validation: %v", counts)
	}
	for _, name := range []string{"archive_record_paths_visited", "archive_metadata_entries_visited"} {
		if counts[name] != 0 {
			t.Errorf("retired filesystem archive work observed: %s=%d", name, counts[name])
		}
	}
	m.sync(t, OutcomeSynced)
	for name, count := range metrics.Counts() {
		if counts[name] != count {
			t.Fatalf("unmeasured cycle changed %s", name)
		}
	}
}

func TestFreshValidationDoesFullWorkAndRetainsNormalReceipts(t *testing.T) {
	m := newMachine(t, "fresh-validation", bareRemote(t))
	archive := archiveMachine(t, &m)
	m.syncer.Archive = &archive
	m.decide(t, "existing", "Existing observation")
	for i := 0; i < 5; i++ {
		result := m.sync(t, OutcomeSynced)
		if result.Unchanged {
			break
		}
	}
	plain, err := m.syncer.Sync(context.Background())
	if err != nil || !plain.Unchanged {
		t.Fatalf("normal baseline: %+v %v", plain, err)
	}
	for range 2 {
		fresh, err := syncwork.WithFreshValidation(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx, work := syncwork.WithWorkMetrics(fresh)
		result, err := m.syncer.Sync(ctx)
		if err != nil || result.Outcome != OutcomeSynced || result.Unchanged || result.ImportedRecords+result.ExportedRecords+result.Collisions+result.Forks+result.SupersessionForks+result.DeferredRecords != 0 {
			t.Fatalf("fresh validation changed records: %+v %v", result, err)
		}
		counts := work.Counts()
		for _, name := range []string{"capture_full_exports", "fallback_git_observation_full", "fallback_publication_journal_full", "fallback_semantic_journal_full", "full_semantic_records"} {
			if counts[name] == 0 {
				t.Fatalf("fresh validation skipped %s: %v", name, counts)
			}
		}
		// Full import may refresh shared dependency generations. Ordinary
		// consumers re-establish their own receipts, then regain their fast path.
		warmed := false
		for i := 0; i < 4; i++ {
			plain, err = m.syncer.Sync(context.Background())
			if err != nil || plain.Outcome != OutcomeSynced {
				t.Fatalf("normal sync after diagnostics: %+v %v", plain, err)
			}
			if plain.Unchanged {
				warmed = true
				break
			}
		}
		if !warmed {
			t.Fatal("fresh validation escaped into ordinary contexts")
		}
	}
}
