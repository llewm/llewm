//go:build !linux

package recordsync

import "os"

// Without a change-time identity, retain the complete validation path.
func fileIdentity(os.FileInfo) (string, bool) { return "", false }
