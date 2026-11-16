// Package permtest changes access to test-owned fixtures so tests exercise
// real privacy rejections and access denials on each platform: POSIX modes on
// Unix, explicit DACLs on Windows, where chmod only toggles read-only.
// Use it only on paths inside a test's temporary directories.
package permtest

import (
	"errors"
	"io"
	"os"
	"testing"
)

// Expose grants read access to everyone on an existing file.
func Expose(t testing.TB, path string) {
	t.Helper()
	if err := expose(path); err != nil {
		t.Fatalf("expose fixture %s: %v", path, err)
	}
}

// Deny removes the current user's access to an existing file or directory
// until the returned restore function runs; restore also runs on cleanup.
// A process that bypasses access control (Unix root, or a Windows
// administrator whose token overrides DACLs, as on hosted CI runners) still
// reads the fixture, so Deny restores it and skips the test rather than
// letting the test assert a denial the platform does not enforce.
func Deny(t testing.TB, path string) (restore func()) {
	t.Helper()
	undo, err := deny(path)
	if err != nil {
		t.Fatalf("deny fixture %s: %v", path, err)
	}
	if readable(path) {
		if err := undo(); err != nil {
			t.Fatalf("restore fixture %s: %v", path, err)
		}
		t.Skipf("access denial on %s is not enforced for this process (%s)", path, privilege())
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

// readable reports whether this process can still read path's contents.
func readable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.IsDir() {
		_, err = f.Readdirnames(1)
	} else {
		_, err = f.Read(make([]byte, 1))
	}
	return err == nil || errors.Is(err, io.EOF)
}
