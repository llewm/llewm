package recordsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
)

// ChangeStamp detects changes to the local archive, including provenance and
// out-of-band edits, without opening or decoding record contents. It is a local
// optimization guard, never an assertion about endpoint delivery or semantics.
// Platforms without inode/change-time metadata decline this optimization.
func (a Archive) ChangeStamp(ctx context.Context) (string, error) {
	root, err := OpenRoot(a.Directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	h := sha256.New()
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Status is bookkeeping, not archive contents or provenance.
		if path == "status" && d.IsDir() {
			return fs.SkipDir
		}
		entries++
		if entries > MaxScanEntries {
			return fmt.Errorf("archive metadata exceeds bounds")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("archive metadata is not regular")
		}
		identity, ok := fileIdentity(info)
		if !ok {
			return fmt.Errorf("archive change identity unavailable")
		}
		fmt.Fprintf(h, "%s\x00%s\x00", filepath.ToSlash(path), identity)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
