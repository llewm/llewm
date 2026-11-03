package database

// Windows does not provide Unix directory fsync through os.File.Sync: Go calls
// FlushFileBuffers, which requires a writable file handle. syncBackup already
// opens the backup read/write and flushes that file, as SQLite's Windows VFS
// does. Do not attempt to flush read-only directory handles or suppress errors
// from the actual backup-file flush. Parent-directory crash durability is not
// separately certified on Windows.
func syncBackupParents(_ string) error { return nil }
