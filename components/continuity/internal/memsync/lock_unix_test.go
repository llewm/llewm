//go:build unix

package memsync

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestClosingLockReleasesItWithAnInheritedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")
	lock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	// A concurrent fork temporarily shares the locked open-file description,
	// even though close-on-exec removes its descriptor once exec completes.
	inherited, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(inherited)
	if other, err := tryLockFile(path); err == nil {
		other.Close()
		t.Fatal("another owner acquired the lock before it was released")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := tryLockFile(path)
	if err != nil {
		t.Fatalf("closed owner left its lock held by an inherited descriptor: %v", err)
	}
	next.Close()
}
