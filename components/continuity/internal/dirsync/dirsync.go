// Package dirsync makes directory entries durable after files are created,
// renamed or removed, using each platform's supported mechanism.
package dirsync

import (
	"errors"
	"os"
)

// Path confirms path names an existing directory, then flushes it (see
// Handle). Open, stat and flush errors are returned unchanged.
func Path(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	return Handle(handle)
}

// Handle confirms an open handle names a directory, then applies the
// platform's directory flush: fsync on Unix, and on Windows nothing further
// (see flush in dirsync_windows.go). Callers must still flush every file they
// wrote through its own handle.
func Handle(handle *os.File) error {
	info, err := handle.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &os.PathError{Op: "sync directory", Path: handle.Name(), Err: errors.New("not a directory")}
	}
	return flush(handle)
}
