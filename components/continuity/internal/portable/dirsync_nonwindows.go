//go:build !windows

package portable

import "os"

// flushDirectory fsyncs an open directory so renamed and newly created entries
// are durable before publication is reported.
func flushDirectory(handle *os.File) error { return handle.Sync() }
