package syncwork

import (
	"context"
	"sync"
)

// WorkMetrics counts work actually attempted, including repeated verification
// and failed attempts. It is opt-in diagnostic state, never a validation receipt.
// Counters contain no record identities, paths, payloads or timing estimates.
type WorkMetrics struct {
	mu     sync.Mutex
	counts map[string]int64
}

type workMetricsKey struct{}

// WithWorkMetrics starts an independent measurement. Without this context,
// RecordWork does nothing. Derived contexts retain the measurement.
func WithWorkMetrics(ctx context.Context) (context.Context, *WorkMetrics) {
	m := &WorkMetrics{counts: make(map[string]int64)}
	return context.WithValue(ctx, workMetricsKey{}, m), m
}

// RecordWork uses static counter names at call sites. A count describes the
// named operation, not unique history or proof of successful semantic import.
func RecordWork(ctx context.Context, name string, count int64) {
	if m, ok := ctx.Value(workMetricsKey{}).(*WorkMetrics); ok {
		m.mu.Lock()
		m.counts[name] += count
		m.mu.Unlock()
	}
}

// Counts returns an independent snapshot, safe even while work is continuing.
func (m *WorkMetrics) Counts() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := make(map[string]int64, len(m.counts))
	for name, count := range m.counts {
		counts[name] = count
	}
	return counts
}
