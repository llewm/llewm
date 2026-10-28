//go:build linux

package recordsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkArchiveChangeStamp(b *testing.B) {
	a, err := OpenArchive(b.TempDir(), lineage)
	if err != nil {
		b.Fatal(err)
	}
	// Match the many small directories/files of a real archive; no live data.
	for i := 0; i < 5669; i++ {
		dir := filepath.Join(a.Directory, "objects", fmt.Sprintf("%08d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			b.Fatal(err)
		}
		for _, name := range []string{"record", "origin"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.ChangeStamp(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
