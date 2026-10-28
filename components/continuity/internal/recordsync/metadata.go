package recordsync

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	return DirectoryChangeStamp(ctx, a.Directory)
}

// DirectoryChangeStamp also supports endpoint guards. It establishes only
// local filesystem stability, never provider delivery or peer receipt.
func DirectoryChangeStamp(ctx context.Context, directory string) (string, error) {
	root, err := OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	h := sha256.New()
	// Reuse a fixed-width binary encoding rather than allocating decimal
	// strings for every inode, timestamp and size in both metadata walks.
	var identityBytes [65]byte
	separator := []byte{0}
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
		for i, value := range identity {
			binary.BigEndian.PutUint64(identityBytes[i*8:], value)
		}
		h.Write([]byte(filepath.ToSlash(path)))
		h.Write(separator)
		h.Write(identityBytes[:])
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
