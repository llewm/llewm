//go:build windows

package memsync

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock, waiting for other syncs on this machine.
// Closing the file releases it.
func lockFile(path string) (*os.File, error) {
	return lockFileMode(path, false)
}

func tryLockFile(path string) (*os.File, error) { return lockFileMode(path, true) }

func lockFileMode(path string, immediate bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Keep the legacy sync lock held while its containing clone is retired.
	// Go's ordinary OpenFile does not request delete-sharing on Windows.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if immediate {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
