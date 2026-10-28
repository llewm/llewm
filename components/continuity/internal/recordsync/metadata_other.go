//go:build !linux

package recordsync

import "os"

// Without a change-time identity, retain the complete validation path.
func fileIdentity(os.FileInfo) ([8]uint64, bool) { return [8]uint64{}, false }
