// Package permtest changes access to test-owned fixtures so tests exercise
// real privacy rejections and access denials on each platform: POSIX modes on
// Unix, explicit DACLs on Windows, where chmod only toggles read-only.
// Use it only on paths inside a test's temporary directories.
package permtest

import "testing"

// Expose grants read access to everyone on an existing file.
func Expose(t testing.TB, path string) {
	t.Helper()
	if err := expose(path); err != nil {
		t.Fatalf("expose fixture %s: %v", path, err)
	}
}

// Deny removes the current user's access to an existing file or directory
// until the returned restore function runs; restore also runs on cleanup.
func Deny(t testing.TB, path string) (restore func()) {
	t.Helper()
	undo, err := deny(path)
	if err != nil {
		t.Fatalf("deny fixture %s: %v", path, err)
	}
	done := false
	restore = func() {
		if done {
			return
		}
		done = true
		if err := undo(); err != nil {
			t.Errorf("restore fixture %s: %v", path, err)
		}
	}
	t.Cleanup(restore)
	return restore
}
