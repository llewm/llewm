package recordsync

import (
	"fmt"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/syncwork"
)

// Force complete observation even on replay: this measures proof-miss work,
// including independent durable audit, rather than the unchanged receipt gate.
func BenchmarkCompleteObservation(b *testing.B) {
	for _, count := range []int{500, 5669} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			ctx, store := archiveStoreFixture(b)
			activateEmptyArchive(b, ctx, store)
			archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
			index := Index{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
			objects := make([]portable.EncodedEnvelope, count)
			for i := range objects {
				objects[i] = archiveObject(b, fmt.Sprintf("record-%05d", i), `{"title":"Cold observation"}`)
			}
			blocks, err := packValidatedBlocks(lineage, objects)
			if err != nil {
				b.Fatal(err)
			}
			if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, blocks); err != nil || !ok {
				b.Fatalf("seed: %v %v", ok, err)
			}
			ctx, work := syncwork.WithWorkMetrics(ctx)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok, err := index.ObservePublicationBatch(ctx, archive, "git:fixture", ObservationStamp{}, true, blocks); err != nil || !ok {
					b.Fatalf("observe: %v %v", ok, err)
				}
			}
			b.StopTimer()
			for key, value := range work.Counts() {
				b.ReportMetric(float64(value)/float64(b.N), key+"/op")
			}
		})
	}
}

// Includes complete integrity audit and endpoint-specific provenance filtering.
func BenchmarkArchiveSnapshot(b *testing.B) {
	for _, count := range []int{500, 5669} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			ctx, store := archiveStoreFixture(b)
			activateEmptyArchive(b, ctx, store)
			archive := Archive{DB: store.DB, ProjectID: store.ProjectID, Lineage: store.Lineage}
			objects := make([]portable.EncodedEnvelope, count)
			for i := range objects {
				objects[i] = archiveObject(b, fmt.Sprintf("record-%05d", i), `{"title":"Cold snapshot"}`)
			}
			if err := archive.CaptureLocal(objects); err != nil {
				b.Fatal(err)
			}
			ctx, work := syncwork.WithWorkMetrics(ctx)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				allowed, all, err := archive.objects(ctx, "git:fixture", false)
				if err != nil || len(allowed) != count || len(all) != count {
					b.Fatalf("snapshot: %d/%d %v", len(allowed), len(all), err)
				}
			}
			b.StopTimer()
			for key, value := range work.Counts() {
				b.ReportMetric(float64(value)/float64(b.N), key+"/op")
			}
		})
	}
}
