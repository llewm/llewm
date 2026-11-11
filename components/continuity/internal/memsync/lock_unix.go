//go:build unix

package memsync

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock, waiting for other syncs on this
// machine. Closing the file releases it.
func lockFile(path string) (*os.File, error) {
	return lockFileMode(path, false)
}

func tryLockFile(path string) (*os.File, error) { return lockFileMode(path, true) }

func lockFileMode(path string, immediate bool) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	flags := unix.LOCK_EX
	if immediate {
		flags |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), flags); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// parentRenameKeepsOpenFiles is true: POSIX renames a directory while files
// inside it are open, so a held lock fences writers across the rename.
const parentRenameKeepsOpenFiles = true
