//go:build unix

package memsync

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock, waiting for other syncs on this
// machine. Closing the file releases it.
func lockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
