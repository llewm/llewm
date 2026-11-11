package portable

import "os"

// Windows does not provide Unix directory fsync through os.File.Sync: Go calls
// FlushFileBuffers, which requires a writable file handle and fails with
// "Access is denied" on the read-only directory handle os.Open returns. This
// matches the database backup rationale (internal/database
// backup_sync_windows.go): publication already flushes every written file
// through its own read/write handle before the directory is renamed into place,
// and syncDirectory still verifies the directory exists. Do not attempt to flush
// directory handles or suppress errors from file flushes. Parent-directory
// crash durability is not separately certified on Windows.
func flushDirectory(_ *os.File) error { return nil }
