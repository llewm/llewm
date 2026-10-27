package portable

import (
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/worknarrative"
)

// BenchmarkMemoryExportUnchanged measures repeated exports after the first
// export retained the portable envelopes. The fixture is intentionally local
// and disposable; it contains no real project data.
func BenchmarkMemoryExportUnchanged(b *testing.B) {
	ctx, db, projectID := operationalProject(b, "sync-benchmark")
	if _, err := (Service{DB: db}).Initialize(ctx, projectID, "lineage:abababababababababababababababab"); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		_, err := (worknarrative.Service{DB: db}).Append(ctx, worknarrative.Record{
			ID: fmt.Sprintf("benchmark-%04d", i), ProjectID: projectID,
			Title: "Benchmark record", Trigger: "manual", Creator: "benchmark",
			Claims: []worknarrative.Claim{{Kind: "outcome", Text: "A representative bounded observation for sync.", Authority: "caller_asserted"}},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	if _, err := EncodeMemoryExport(ctx, db, projectID); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeMemoryExport(ctx, db, projectID); err != nil {
			b.Fatal(err)
		}
	}
}
