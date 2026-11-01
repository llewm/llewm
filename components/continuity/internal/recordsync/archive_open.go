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
		activate, err := archiveRouteWithin(ctx, store, tx, legacyDirectory)
		if err != nil {
			return err
		}
		if !activate {
			return nil
		}
		empty := archiveInventory{EnvelopesSHA256: Hash(""), OriginsSHA256: Hash("")}
		return store.activateWithin(ctx, tx, empty)
	})
}

// archiveRouteWithin performs only reads. Its true result permits the normal
// opener to initialize an empty fresh identity; an auditor must refuse it.
func archiveRouteWithin(ctx context.Context, store archiveStore, tx *sql.Tx, legacyDirectory string) (bool, error) {
	stamp, err := store.stampWithin(ctx, tx, "")
	if err != nil {
		return false, err
	}
	var status string
	var fresh bool
	var body, checksum sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,fresh_identity,substr(migration_json,1,4097),substr(migration_checksum,1,65) FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&status, &fresh, &body, &checksum); err != nil {
		return false, err
	}
	if body.Valid || checksum.Valid {
		if status != "ready" {
			return false, errArchiveNotReady
		}
		receipt, err := decodeArchiveCutover(store, body, checksum)
		if err != nil {
			return false, err
		}
		root, err := OpenRoot(filepath.Dir(legacyDirectory))
		if err != nil {
			return false, err
		}
		defer root.Close()
		expected := receipt.fence()
		actual, err := ReadFile(root, filepath.Base(legacyDirectory), len(expected))
		if err != nil || !bytes.Equal(actual, expected) {
			return false, fmt.Errorf("archive migration fence is missing or changed; resume verified cutover")
		}
		return false, nil
	}
	if !fresh {
		return false, errArchiveNotReady
	}
	if _, err := os.Lstat(legacyDirectory); !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("existing legacy archive requires explicit migration")
	}
	if status == "ready" {
		return false, nil
	}
	if stamp.Records != 0 || stamp.Origins != 0 || stamp.Changes != 0 {
		return false, errArchiveNotReady
	}
	return true, nil
}
