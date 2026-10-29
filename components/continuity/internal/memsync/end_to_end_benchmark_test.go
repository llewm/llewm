package memsync

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/checkpoint"
	"github.com/llewm/llewm/components/continuity/internal/foldersync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
	"github.com/llewm/llewm/components/continuity/internal/resource"
	"github.com/llewm/llewm/components/continuity/internal/trackplan"
)

// These disposable local fixtures time a complete warmed cycle, including
// export, archive discovery, block publication, import and Git operations.
// Filter CPU profiles with -tagfocus=sync_phase=measured to omit fixture setup,
// warm-up and the canonical writes between measured cycles.
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
						pprof.Do(ctx, pprof.Labels("sync_phase", "measured"), func(context.Context) {
							if err := cycle(); err != nil {
								b.Fatal(err)
							}
						})
					}
				})
			}
		}
	}
}

// Repeated, text-heavy plan revisions exercise the export/validation/redaction
// costs absent from decision-only fixtures. All history here is synthetic.
func BenchmarkHistorySyncPlanHistory(b *testing.B) {
	benchmarkHistorySyncPlanHistory(b, false, false)
}

// A realistic local arrival includes a plan revision, resource observation,
// checkpoint context and action history, rather than only a decision.
func BenchmarkHistorySyncMixedHistory(b *testing.B) {
	benchmarkHistorySyncPlanHistory(b, true, false)
}

// Remote arrivals measure the receiving Git cycle, including semantic appends.
func BenchmarkHistorySyncRemoteMixedHistory(b *testing.B) {
	benchmarkHistorySyncPlanHistory(b, true, true)
}

func benchmarkHistorySyncPlanHistory(b *testing.B, mixed, remote bool) {
	ctx := context.Background()
	for _, size := range []int{500, 5669} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			origin := bareRemote(b)
			m := newMachine(b, "plan-history", origin)
			receiver := m
			if remote {
				receiver = newMachine(b, "remote-plan-history", origin)
			}
			for i := 0; i < size; i++ {
				m.decide(b, fmt.Sprintf("decision-%04d", i), "Observation")
			}
			var items []trackplan.Item
			for i := 0; i < 6; i++ {
				items = append(items, trackplan.Item{ID: fmt.Sprintf("item-%d", i), Position: i, Title: "Qualify history", Status: "active", Detail: strings.Repeat("Status: preserve canonical validation, token rotation evidence, repair and attribution. ", 20)})
			}
			service := trackplan.Service{DB: m.db}
			for i := 0; i < 20; i++ {
				if _, err := service.AppendPlan(ctx, trackplan.Plan{ProjectID: m.project, ID: "history-plan", RoadmapNodeID: "history", Title: "Synthetic plan history", Summary: "Profile repeated historical text", Status: "active", Creator: "benchmark", Items: items}); err != nil {
					b.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				m.sync(b, OutcomeSynced)
				if remote {
					receiver.sync(b, OutcomeSynced)
				}
			}
			b.ResetTimer()
			previousCheckpoint := ""
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if mixed {
					if _, err := service.AppendPlan(ctx, trackplan.Plan{ProjectID: m.project, ID: "history-plan", RoadmapNodeID: "history", Title: "Synthetic plan history", Summary: "Mixed arrival", Status: "active", Creator: "benchmark", Items: items}); err != nil {
						b.Fatal(err)
					}
					if _, err := (resource.Service{DB: m.db}).Record(ctx, resource.Resource{ProjectID: m.project, ID: "fixture-host", Kind: "host", Title: "Synthetic host", Summary: fmt.Sprintf("Observation %d", i), Creator: "benchmark"}); err != nil {
						b.Fatal(err)
					}
					saved, err := (checkpoint.Service{DB: m.db}).CreateWithActions(ctx, checkpoint.Checkpoint{ProjectID: m.project, TaskID: "mixed-history", PredecessorCheckpointID: previousCheckpoint, Task: "Synthetic qualification", Status: "in_progress", Summary: "Mixed arrival", NextAction: "Continue qualification"})
					if err != nil {
						b.Fatal(err)
					}
					previousCheckpoint = saved.Checkpoint.ID
				} else {
					m.decide(b, fmt.Sprintf("arrival-%d", i), "New observation")
				}
				if remote {
					m.sync(b, OutcomeSynced)
				}
				b.StartTimer()
				pprof.Do(ctx, pprof.Labels("sync_phase", "measured"), func(context.Context) {
					result := receiver.sync(b, OutcomeSynced)
					if remote {
						if result.Unchanged || result.ImportedRecords < 1 || result.ExportedRecords != 0 {
							b.Fatalf("unexpected remote cycle: %+v", result)
						}
						return
					}
					if result.Unchanged || result.ExportedRecords < 1 || (!mixed && result.ExportedRecords != 1) || result.ImportedRecords != 0 {
						b.Fatalf("unexpected plan-history cycle: %+v", result)
					}
				})
			}
		})
	}
}

// Foreign arrivals are retained locally but cannot be relayed to this endpoint.
// Seed their origin before capture, as an independent transport would, and time
// the complete Git cycle after each arrival. Fixture writes stay outside both
// the timer and the measured CPU label.
func BenchmarkHistorySyncForeignResidual(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{500, 5669} {
		b.Run(fmt.Sprintf("records_%d", size), func(b *testing.B) {
			m := newMachine(b, "foreign-benchmark", bareRemote(b))
			archive, err := recordsync.OpenArchive(filepath.Join(b.TempDir(), "archive"), testLineage)
			if err != nil {
				b.Fatal(err)
			}
			m.syncer.Archive = &archive
			captureForeign := func() {
				export, err := portable.EncodeMemoryExport(ctx, m.db, m.project)
				if err != nil {
					b.Fatal(err)
				}
				for _, object := range export.Envelopes {
					if _, err := archive.Put(object, "git:independent-fixture"); err != nil {
						b.Fatal(err)
					}
				}
			}
			for i := 0; i < size; i++ {
				m.decide(b, fmt.Sprintf("foreign-%04d", i), "Foreign observation")
			}
			captureForeign()
			for i := 0; i < 2; i++ {
				m.sync(b, OutcomeSynced)
			}
			var before int
			if err := m.db.QueryRow(`SELECT COUNT(*) FROM history_index_records`).Scan(&before); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				m.decide(b, fmt.Sprintf("foreign-new-%06d", i), "Foreign arrival")
				captureForeign()
				b.StartTimer()
				pprof.Do(ctx, pprof.Labels("sync_phase", "measured"), func(context.Context) {
					result := m.sync(b, OutcomeSynced)
					if result.Unchanged || result.ExportedRecords != 0 {
						b.Fatalf("foreign arrival skipped or relayed: %+v", result)
					}
				})
			}
			b.StopTimer()
			var rows, unique int
			if err := m.db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT envelope_hash) FROM history_index_records`).Scan(&rows, &unique); err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(rows-before)/float64(b.N), "index_rows/arrival")
			b.ReportMetric(float64(rows)/float64(unique), "rows/unique")
		})
	}
}

// Compare a proof hit with a missing proof and a changed build identity. Only
// the build identity changes in build_miss; existing derived inventories and
// semantic replay proofs remain available. Setup and invalidation stay outside
// both the timer and the measured profile labels. Do not run these sub-benchmarks
// in parallel: buildinfo.Commit represents the process-wide executable identity.
func BenchmarkHistorySyncUnchangedGate(b *testing.B) {
	ctx := context.Background()
	for _, mode := range []string{"hit", "deleted", "build_miss"} {
		b.Run(mode, func(b *testing.B) {
			original := buildinfo.Commit
			b.Cleanup(func() { buildinfo.Commit = original })
			buildinfo.Commit = "fixture-warm"
			m := newMachine(b, "gate-benchmark", bareRemote(b))
			archive, err := recordsync.OpenArchive(filepath.Join(b.TempDir(), "archive"), testLineage)
			if err != nil {
				b.Fatal(err)
			}
			m.syncer.Archive = &archive
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
				b.StopTimer()
				if mode == "deleted" {
					if _, err := m.db.Exec(`DELETE FROM history_sync_proofs`); err != nil {
						b.Fatal(err)
					}
				}
				if mode == "build_miss" {
					buildinfo.Commit = fmt.Sprintf("fixture-build-%d", i)
				}
				b.StartTimer()
				pprof.Do(ctx, pprof.Labels("sync_phase", "measured", "sync_case", mode), func(context.Context) {
					result, err := m.syncer.Sync(ctx)
					if err != nil || result.Outcome != OutcomeSynced || result.Unchanged != (mode == "hit") || result.ImportedRecords != 0 || result.ExportedRecords != 0 {
						b.Fatalf("unexpected cycle: %+v %v", result, err)
					}
				})
			}
		})
	}
}

func BenchmarkFolderSyncUnchangedGate(b *testing.B) {
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("proof_%t", cached), func(b *testing.B) {
			m := newMachine(b, "folder-gate-benchmark", bareRemote(b))
			archive, err := recordsync.OpenArchive(filepath.Join(b.TempDir(), "archive"), testLineage)
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 5669; i++ {
				m.decide(b, fmt.Sprintf("record-%04d", i), "Observation")
			}
			directory := b.TempDir()
			marker, err := foldersync.Create(directory)
			if err != nil {
				b.Fatal(err)
			}
			s := foldersync.Syncer{DB: m.db, ProjectID: m.project, Archive: archive, Endpoint: foldersync.Endpoint{Name: "bench", Directory: directory, StoreID: marker.StoreID, LineageID: testLineage}}
			if err := s.Convert(ctx); err != nil {
				b.Fatal(err)
			}
			cycle := func() (foldersync.Result, error) {
				if _, err := archive.CaptureProject(ctx, m.db, m.project); err != nil {
					return foldersync.Result{}, err
				}
				return s.Sync(ctx)
			}
			for i := 0; i < 5; i++ {
				if _, err := cycle(); err != nil {
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
				result, err := cycle()
				if err != nil || result.Unchanged != cached {
					b.Fatalf("cycle: %+v %v", result, err)
				}
			}
		})
	}
}
