package syncwork

import (
	"context"
	"sync"
	"testing"
)

func TestWorkMetricsStayLocalAndIncludeDerivedContexts(t *testing.T) {
	parent := context.Background()
	ctx, metrics := WithWorkMetrics(parent)
	other, isolated := WithWorkMetrics(ctx)
	derived, cancel := context.WithCancel(ctx)
	cancel()
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				RecordWork(context.WithoutCancel(derived), "attempts", 1)
				_ = metrics.Counts()
			}
		})
	}
	workers.Wait()
	RecordWork(other, "attempts", 3)
	RecordWork(parent, "attempts", 1000)
	counts := metrics.Counts()
	if counts["attempts"] != 800 || isolated.Counts()["attempts"] != 3 {
		t.Fatalf("measurements leaked or lost work: %v %v", counts, isolated.Counts())
	}
	counts["attempts"] = -1
	if metrics.Counts()["attempts"] != 800 {
		t.Fatal("returned snapshot aliases the live counters")
	}
}
