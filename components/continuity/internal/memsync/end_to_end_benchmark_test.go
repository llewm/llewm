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
	for _, transport := range []string{"git", "folder"} {
		for _, changed := range []bool{false, true} {
			name := transport + "/unchanged"
			if changed {
				name = transport + "/one_new"
			}
			b.Run(name, func(b *testing.B) {
				m := newMachine(b, "benchmark", bareRemote(b))
				archive, err := recordsync.OpenArchive(filepath.Join(b.TempDir(), "archive"), testLineage)
				if err != nil {
					b.Fatal(err)
				}
				m.syncer.Archive = &archive
				for i := 0; i < 500; i++ {
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
