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
	for _, name := range []string{"operational_specs_loaded", "snapshot_envelope_encodes", "published_block_bytes", "block_read_bytes", "inventory_records_checked", "archive_record_paths_visited", "archive_metadata_entries_visited", "git_tree_entries_visited"} {
		if counts[name] <= 0 {
			t.Errorf("missing observed work %s: %v", name, counts)
		}
	}
	m.sync(t, OutcomeSynced)
	for name, count := range metrics.Counts() {
		if counts[name] != count {
			t.Fatalf("unmeasured cycle changed %s", name)
		}
	}
}
