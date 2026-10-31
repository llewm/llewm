package memsync

import (
	"context"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/recordsync"
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
	ctx, metrics := recordsync.WithWorkMetrics(context.Background())
	result, err := m.syncer.Sync(ctx)
	if err != nil || result.Outcome != OutcomeSynced || result.ExportedRecords != 1 || result.ImportedRecords != 0 {
		t.Fatalf("measured cycle changed behavior: %+v %v", result, err)
	}
	counts := metrics.Counts()
	for _, name := range []string{"published_block_bytes", "block_read_bytes", "inventory_records_checked", "archive_record_paths_visited", "archive_metadata_entries_visited", "git_tree_entries_visited"} {
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
