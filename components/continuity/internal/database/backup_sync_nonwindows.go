//go:build !windows

package database

import (
	"os"
	"path/filepath"
)

func syncBackupParents(path string) error {
	// Backup may have created several parent directories. Persist their
	// entries too, rather than only the file's immediate directory.
	for name := filepath.Dir(path); ; name = filepath.Dir(name) {
		parent, err := os.Open(name)
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if filepath.Dir(name) == name {
			return nil
		}
	}
}
