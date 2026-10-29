package recordsync

import (
	"fmt"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/portable"
)

// Measure inventory reconstruction separately from indexing and semantic import.
func BenchmarkBlockInventory(b *testing.B) {
	ctx, db, project := indexedTestProject(b, "inventory-benchmark")
	var objects []portable.EncodedEnvelope
	for i := 0; i < 5669; i++ {
		object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{
			LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1,
			RecordID: fmt.Sprintf("record-%04d", i), RecordRevision: 1,
			RecordedAt: time.Unix(1234567890, 0), Authority: "caller_asserted",
			Payload: []byte(`{"title":"Benchmark observation"}`),
			Refs:    []portable.Reference{{Kind: "decision", ID: "target"}},
		})
		if err != nil {
			b.Fatal(err)
		}
		objects = append(objects, object)
	}
	blocks, err := packValidatedBlocks(lineage, objects)
	if err != nil {
		b.Fatal(err)
	}
	index := Index{DB: db, ProjectID: project, Lineage: lineage}
	for _, block := range blocks {
		if err := index.AddBlock(ctx, block); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := index.blockObjects(ctx, blocks)
		if err != nil || len(got) != len(objects) {
			b.Fatalf("inventory: %d objects, %v", len(got), err)
		}
	}
}
