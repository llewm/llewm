package memsync

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

// These disposable local fixtures time a complete warmed cycle, including
// export, archive discovery, block publication, import and Git operations.
func BenchmarkHistorySyncEndToEnd(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{500, 5669} {
		for _, transport := range []string{"git", "folder"} {
			for _, changed := range []bool{false, true} {
				name := transport + "/unchanged"
				if changed {
					name = transport + "/one_new"
				}
				b.Run(fmt.Sprintf("records_%d/%s", size, name), func(b *testing.B) {
					m := newMachine(b, "benchmark", bareRemote(b))
					archive, err := recordsync.OpenArchive(filepath.Join(b.TempDir(), "archive"), testLineage)
					if err != nil {
						b.Fatal(err)
					}
					m.syncer.Archive = &archive
					for i := 0; i < size; i++ {
						m.decide(b, fmt.Sprintf("benchmark-%04d", i), "Benchmark observation")
					}
					var cycle func() error
					if transport == "git" {
						cycle = func() error {
							_, err := m.syncer.Sync(ctx)
							return err
						}
					} else {
						directory := b.TempDir()
						marker, err := foldersync.Create(directory)
						if err != nil {
							b.Fatal(err)
						}
						syncer := foldersync.Syncer{DB: m.db, ProjectID: m.project, Archive: archive, Endpoint: foldersync.Endpoint{Name: "bench", Directory: directory, StoreID: marker.StoreID, LineageID: testLineage}}
						if err := syncer.Convert(ctx); err != nil {
							b.Fatal(err)
						}
						cycle = func() error {
							export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
							if err == nil {
								err = archive.CaptureLocal(export.Envelopes)
							}
							if err == nil {
								_, err = syncer.Sync(ctx)
							}
							return err
						}
					}
					if err := cycle(); err != nil {
						b.Fatal(err)
					}
					// A second unchanged cycle establishes any conservative semantic
					// replay proof before measuring the steady state.
					if err := cycle(); err != nil {
						b.Fatal(err)
					}
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if changed {
							b.StopTimer()
							m.decide(b, fmt.Sprintf("new-%06d", i), "New observation")
							b.StartTimer()
						}
						if err := cycle(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

// Compare the same steady-state workflow with and without a persisted
// completion proof. Proof deletion is outside the measured full-cycle time.
func BenchmarkHistorySyncUnchangedGate(b *testing.B) {
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("proof_%t", cached), func(b *testing.B) {
			m := newMachine(b, "gate-benchmark", bareRemote(b))
			for i := 0; i < 5669; i++ {
				m.decide(b, fmt.Sprintf("record-%04d", i), "Observation")
			}
			for i := 0; i < 4; i++ {
				if _, err := m.syncer.Sync(ctx); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					b.StopTimer()
					if _, err := m.db.Exec(`DELETE FROM history_sync_proofs`); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
				result, err := m.syncer.Sync(ctx)
				if err != nil || result.Unchanged != cached {
					b.Fatalf("unexpected cycle: %+v %v", result, err)
				}
			}
		})
	}
}
