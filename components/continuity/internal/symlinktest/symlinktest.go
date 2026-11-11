// Package symlinktest creates symlink fixtures for tests that exercise symlink
// rejection, skipping only where the platform withholds the privilege.
package symlinktest

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
)

// errPrivilegeNotHeld is Windows ERROR_PRIVILEGE_NOT_HELD, returned when an
// ordinary user without Developer Mode lacks SeCreateSymbolicLinkPrivilege.
const errPrivilegeNotHeld = syscall.Errno(1314)

// Create makes link point at target. On Windows without symlink privilege the
// test is skipped with an explicit reason, so the missing coverage stays
// visible; every other failure is fatal.
func Create(t testing.TB, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if Unavailable(err) {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	t.Fatal(err)
}

// Unavailable reports whether err means the platform refused to create a
// symlink because the current user lacks the privilege.
func Unavailable(err error) bool {
	return runtime.GOOS == "windows" && errors.Is(err, errPrivilegeNotHeld)
}
