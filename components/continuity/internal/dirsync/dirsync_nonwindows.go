//go:build !windows

package dirsync

import "os"

// flush fsyncs an open directory so created, renamed and removed entries are
// durable before the caller reports success.
func flush(handle *os.File) error { return handle.Sync() }
