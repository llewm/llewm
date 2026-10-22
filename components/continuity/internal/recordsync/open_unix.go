//go:build unix

package recordsync

import (
	"golang.org/x/sys/unix"
	"os"
)

// A racing replacement with a FIFO must not turn a bounded transport read into
// an indefinitely blocking open. os.Root also confines ancestor resolution.
func openRead(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
