//go:build unix

package memsync

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock, waiting for other syncs on this
// machine. Closing the file releases it.
func lockFile(path string) (*fileLock, error) {
	return lockFileMode(path, false)
}

func tryLockFile(path string) (*fileLock, error) { return lockFileMode(path, true) }

func lockFileMode(path string, immediate bool) (*fileLock, error) {
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
	return &fileLock{file}, nil
}

func (file *fileLock) Close() error {
	// Closing alone leaves flock held while another process retains a copy
	// of this description, including a concurrent fork before close-on-exec.
	err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return errors.Join(err, file.File.Close())
}

// parentRenameKeepsOpenFiles is true: POSIX renames a directory while files
// inside it are open, so a held lock fences writers across the rename.
const parentRenameKeepsOpenFiles = true
