package recordsync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// openArchiveStore is the runtime gate for the SQLite Archive.
// New identities have an atomic staging marker from their identity insertion;
// only that proof and an absent legacy route permit empty initialization.
// Existing identities and interrupted cutovers never become empty archives by
// inference. Normal opens check constant metadata/fence bytes, not old history.
func openArchiveStore(ctx context.Context, store archiveStore, legacyDirectory string) error {
	if !filepath.IsAbs(legacyDirectory) {
		return fmt.Errorf("absolute legacy archive route required")
	}
	return store.transact(ctx, func(tx *sql.Tx) error {
		stamp, err := store.stampWithin(ctx, tx, "")
		if err != nil {
			return err
		}
		var status string
		var fresh bool
		var body, checksum sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT status,fresh_identity,substr(migration_json,1,4097),substr(migration_checksum,1,65) FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&status, &fresh, &body, &checksum); err != nil {
			return err
		}
		if body.Valid || checksum.Valid {
			if status != "ready" {
				return errArchiveNotReady
			}
			receipt, err := decodeArchiveCutover(store, body, checksum)
			if err != nil {
				return err
			}
			root, err := OpenRoot(filepath.Dir(legacyDirectory))
			if err != nil {
				return err
			}
			defer root.Close()
			expected := receipt.fence()
			actual, err := ReadFile(root, filepath.Base(legacyDirectory), len(expected))
			if err != nil || !bytes.Equal(actual, expected) {
				return fmt.Errorf("archive migration fence is missing or changed; resume verified cutover")
			}
			return nil
		}
		if !fresh {
			return errArchiveNotReady
		}
		if _, err := os.Lstat(legacyDirectory); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("existing legacy archive requires explicit migration")
		}
		if status == "ready" {
			return nil
		}
		if stamp.Records != 0 || stamp.Origins != 0 || stamp.Changes != 0 {
			return errArchiveNotReady
		}
		empty := archiveInventory{EnvelopesSHA256: Hash(""), OriginsSHA256: Hash("")}
		return store.activateWithin(ctx, tx, empty)
	})
}
