package recordsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/database"
)

type archiveCutoverPaths struct{ Source, Retained, DatabaseBackup string }

type archiveCutoverReceipt struct {
	Version                                       int
	Project, Lineage, PathsSHA256, DatabaseSHA256 string
	Inventory                                     archiveInventory
}

func (p archiveCutoverPaths) normalized() (archiveCutoverPaths, error) {
	values := []*string{&p.Source, &p.Retained, &p.DatabaseBackup}
	for _, value := range values {
		if strings.TrimSpace(*value) == "" || strings.ContainsRune(*value, 0) {
			return p, fmt.Errorf("explicit archive and backup paths required")
		}
		absolute, err := filepath.Abs(*value)
		if err != nil {
			return p, err
		}
		// Resolve existing parent aliases before testing overlap and binding the
		// receipt. Backup directories must be prepared by the coordinator; an
		// alias must never let a new backup be written inside the legacy source.
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return p, err
		}
		*value = filepath.Join(parent, filepath.Base(absolute))
	}
	if p.Source == p.Retained || filepath.Dir(p.Source) != filepath.Dir(p.Retained) {
		return p, fmt.Errorf("retained archive must be a distinct sibling of source")
	}
	for _, directory := range []string{p.Source, p.Retained} {
		relative, err := filepath.Rel(directory, p.DatabaseBackup)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return p, fmt.Errorf("database backup must be outside archive paths")
		}
	}
	return p, nil
}

func (p archiveCutoverPaths) hash() string {
	// Preserve exact filesystem path bytes, including non-UTF8 Unix names;
	// encoding/json would normalize invalid UTF8 and weaken this binding.
	return Hash("archive-cutover-paths-v1\x00" + p.Source + "\x00" + p.Retained + "\x00" + p.DatabaseBackup)
}

func (r archiveCutoverReceipt) bytes() []byte {
	body, _ := json.Marshal(r)
	return body
}

func (r archiveCutoverReceipt) fence() []byte {
	return []byte("llewm-history-archive-migrated-v1\n" + Hash(string(r.bytes())) + "\n")
}

// prepareArchiveCutover requires the coordinator to have stopped every writer.
// It makes its own verified SQLite backup, stages authoritative source bytes and
// saves an immutable recovery receipt atomically. It does not fence or activate.
// Failed attempts retain their backups; existing files are never overwritten.
func prepareArchiveCutover(ctx context.Context, store archiveStore, paths archiveCutoverPaths) (archiveCutoverReceipt, error) {
	var receipt archiveCutoverReceipt
	paths, err := paths.normalized()
	if err != nil {
		return receipt, err
	}
	receipt, found, err := readArchiveCutover(ctx, store, paths)
	if err != nil || found {
		return receipt, err
	}
	if _, err := os.Lstat(paths.Retained); !errors.Is(err, os.ErrNotExist) {
		return receipt, fmt.Errorf("retained archive destination already exists or cannot be inspected")
	}
	source, err := collectArchiveMigration(ctx, paths.Source, store.Lineage)
	if err != nil {
		return receipt, err
	}
	var dbPath string
	if err := store.DB.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		return receipt, err
	}
	if _, err := database.Backup(ctx, dbPath, paths.DatabaseBackup); err != nil {
		return receipt, err
	}
	digest, err := verifyArchiveCutoverBackup(ctx, store, paths.DatabaseBackup)
	if err != nil {
		return receipt, err
	}
	after, err := collectArchiveMigration(ctx, paths.Source, store.Lineage)
	if err != nil || after.inventory != source.inventory {
		return receipt, fmt.Errorf("legacy archive changed while preparing backup")
	}
	receipt = archiveCutoverReceipt{Version: 1, Project: store.ProjectID, Lineage: store.Lineage, PathsSHA256: paths.hash(), DatabaseSHA256: digest, Inventory: source.inventory}
	err = store.transact(ctx, func(tx *sql.Tx) error {
		if err := source.stageWithin(ctx, tx, store); err != nil {
			return err
		}
		body := receipt.bytes()
		result, err := tx.ExecContext(ctx, `UPDATE history_archive_state SET migration_json=?,migration_checksum=? WHERE project_id=? AND status='staging' AND migration_json IS NULL`, string(body), Hash(string(body)), store.ProjectID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return fmt.Errorf("archive cutover preparation changed concurrently")
		}
		return nil
	})
	return receipt, err
}

// completeArchiveCutover resumes any prepared cutover, including a crash between
// rename, fence publication and SQLite activation. It never restores or deletes
// a retained source and refuses any competing recreated source directory.
func completeArchiveCutover(ctx context.Context, store archiveStore, paths archiveCutoverPaths) error {
	paths, err := paths.normalized()
	if err != nil {
		return err
	}
	receipt, found, err := readArchiveCutover(ctx, store, paths)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("verified archive cutover preparation required")
	}
	// Audit before changing the filesystem too: damaged staging is never a
	// reason to retire the still-usable legacy source.
	if err := store.transact(ctx, func(tx *sql.Tx) error {
		audit, err := store.auditWithin(ctx, tx)
		if err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&status); err != nil {
			return err
		}
		if status == "staging" && audit.Inventory != receipt.Inventory {
			return fmt.Errorf("staged archive differs from cutover preparation")
		}
		return nil
	}); err != nil {
		return err
	}
	root, err := OpenRoot(filepath.Dir(paths.Source))
	if err != nil {
		return err
	}
	defer root.Close()
	sourceName, retainedName := filepath.Base(paths.Source), filepath.Base(paths.Retained)
	source, sourceErr := root.Lstat(sourceName)
	retained, retainedErr := root.Lstat(retainedName)
	if sourceErr == nil && source.IsDir() && source.Mode()&os.ModeSymlink == 0 && errors.Is(retainedErr, os.ErrNotExist) {
		// Revalidate immediately before the irreversible namespace change.
		verified, err := collectArchiveMigration(ctx, paths.Source, store.Lineage)
		if err != nil || verified.inventory != receipt.Inventory {
			return fmt.Errorf("legacy archive no longer matches prepared cutover")
		}
		if err := root.Rename(sourceName, retainedName); err != nil {
			return err
		}
	} else if retainedErr != nil || !retained.IsDir() || retained.Mode()&os.ModeSymlink != 0 || (sourceErr == nil && !source.Mode().IsRegular()) || (sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist)) {
		return fmt.Errorf("archive cutover namespace differs from prepared recovery state")
	}
	if err := syncArchiveParent(root); err != nil {
		return err
	}
	if err := publishArchiveFence(root, sourceName, receipt.fence()); err != nil {
		return err
	}
	// The rename retains exact originals. Verify that complete source again,
	// not an index, before the final SQLite activation transaction.
	verified, err := collectArchiveMigration(ctx, paths.Retained, store.Lineage)
	if err != nil || verified.inventory != receipt.Inventory {
		return fmt.Errorf("retained archive differs from prepared cutover")
	}
	return store.transact(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&status); err != nil {
			return err
		}
		if status == "ready" {
			_, err := store.auditWithin(ctx, tx)
			return err
		}
		return store.activateWithin(ctx, tx, receipt.Inventory)
	})
}

func readArchiveCutover(ctx context.Context, store archiveStore, paths archiveCutoverPaths) (archiveCutoverReceipt, bool, error) {
	var receipt archiveCutoverReceipt
	var body, checksum sql.NullString
	err := store.DB.QueryRowContext(ctx, `SELECT substr(migration_json,1,4097),substr(migration_checksum,1,65) FROM history_archive_state WHERE project_id=?`, store.ProjectID).Scan(&body, &checksum)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !body.Valid && !checksum.Valid) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	receipt, err = decodeArchiveCutover(store, body, checksum)
	if err != nil || receipt.PathsSHA256 != paths.hash() {
		return receipt, false, fmt.Errorf("archive cutover recovery receipt is invalid or belongs to other paths")
	}
	digest, err := archiveBackupDigest(ctx, paths.DatabaseBackup)
	if err != nil || digest != receipt.DatabaseSHA256 {
		return receipt, false, fmt.Errorf("archive cutover database backup is missing or changed")
	}
	return receipt, true, nil
}

func decodeArchiveCutover(store archiveStore, body, checksum sql.NullString) (archiveCutoverReceipt, error) {
	var receipt archiveCutoverReceipt
	decoder := json.NewDecoder(strings.NewReader(body.String))
	decoder.DisallowUnknownFields()
	if !body.Valid || !checksum.Valid || len(body.String) > 4096 || Hash(body.String) != checksum.String || decoder.Decode(&receipt) != nil || !bytes.Equal(receipt.bytes(), []byte(body.String)) || receipt.Version != 1 || receipt.Project != store.ProjectID || receipt.Lineage != store.Lineage || !hex64.MatchString(receipt.PathsSHA256) || !hex64.MatchString(receipt.DatabaseSHA256) {
		return receipt, fmt.Errorf("archive cutover recovery receipt is invalid")
	}
	return receipt, nil
}

func publishArchiveFence(root *os.Root, name string, body []byte) error {
	// Stage, fsync and publish exclusively: a crash cannot expose a partial
	// fence which would make the next recovery attempt ambiguous.
	_, err := WriteFile(root, name, body, false)
	return err
}

func syncArchiveParent(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveBackupDigest(ctx context.Context, path string) (string, error) {
	root, err := OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("database backup must be an existing regular file")
	}
	file, err := openRead(root, name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || !actual.Mode().IsRegular() {
		return "", fmt.Errorf("database backup changed while opening")
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		h.Write(buffer[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyArchiveCutoverBackup(ctx context.Context, store archiveStore, path string) (string, error) {
	before, err := archiveBackupDigest(ctx, path)
	if err != nil {
		return "", err
	}
	backup, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return "", err
	}
	defer backup.Close()
	var integrity, lineage string
	if err := backup.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return "", fmt.Errorf("archive cutover backup integrity verification failed")
	}
	if err := backup.QueryRowContext(ctx, `SELECT lineage_id FROM project_memory_identities WHERE project_id=?`, store.ProjectID).Scan(&lineage); err != nil || lineage != store.Lineage {
		return "", fmt.Errorf("archive cutover backup has a different project identity")
	}
	rows, err := backup.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return "", err
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil || invalid {
		return "", fmt.Errorf("archive cutover backup has foreign key violations")
	}
	after, err := archiveBackupDigest(ctx, path)
	if err != nil || before != after {
		return "", fmt.Errorf("archive cutover backup changed during verification")
	}
	return after, nil
}

// Pin the connection so a failed SQLite COMMIT cannot return an active native
// transaction to the pool after database/sql has already finalized its Tx.
func (s archiveStore) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reserve the SQLite writer before any read snapshot can need an upgrade.
	// Concurrent immutable ingestions must serialize, rather than fail with
	// SQLITE_BUSY_SNAPSHOT after reading their initial archive stamp.
	if _, err := tx.ExecContext(ctx, `UPDATE history_archive_state SET status=status WHERE project_id=? AND 0`, s.ProjectID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return database.CommitPinned(ctx, conn, tx)
}

func (s archiveStore) view(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}
