package memsync

import (
	"context"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

func TestConsecutiveCheckpointArrivalsUseDeltaCapture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newMachine(t, "checkpoint-delta", bareRemote(t))
	m.decide(t, "baseline", "Existing history")
	for i := 0; i < 2; i++ {
		m.sync(t, OutcomeSynced)
	}
	previous := ""
	for i := 0; i < 3; i++ {
		saved, err := (checkpoint.Service{DB: m.db}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: m.project, TaskID: "checkpoint-delta", PredecessorCheckpointID: previous, Task: "Qualification", Status: "in_progress", Summary: "New checkpoint", NextAction: "Continue"})
		if err != nil {
			t.Fatal(err)
		}
		previous = saved.Checkpoint.ID
		measured, metrics := syncwork.WithWorkMetrics(ctx)
		result, err := m.syncer.Sync(measured)
		if err != nil || result.Outcome != OutcomeSynced || result.Unchanged || result.ExportedRecords == 0 || result.ImportedRecords != 0 || result.Collisions+result.Forks+result.DeferredRecords != 0 {
			t.Fatalf("cycle%d: %+v %v", i, result, err)
		}
		counts := metrics.Counts()
		if counts["operational_delta_loads"] != 1 || counts["capture_full_exports"] != 0 || counts["fallback_capture_global_change"] != 0 || counts["fallback_capture_publication_receipt"] != 0 {
			t.Fatalf("cycle%d did not acknowledge selected capture: %v", i, counts)
		}
	}
}
