package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// Flush the completed SQLite file before a recovery receipt or schema transition
// can depend on it, then perform the platform's parent-directory synchronization.
func syncBackup(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncBackupParents(path)
}
func backupDigest(ctx context.Context, path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return "", fmt.Errorf("regular backup file required")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(before, actual) {
		return "", fmt.Errorf("backup identity changed before hashing")
	}
	h := sha256.New()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			h.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		return "", fmt.Errorf("backup identity changed while hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyBackup checks exact prepared bytes before and after SQLite integrity,
// foreign-key and schema validation. It never creates or modifies the backup.
func VerifyBackup(ctx context.Context, path, expectedMigration, expectedSHA256 string) (BackupResult, error) {
	result := BackupResult{BackupPath: path}
	digest, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(expectedSHA256) != expectedSHA256 || expectedMigration == "" {
		return result, fmt.Errorf("exact backup schema and SHA256 required")
	}
	before, err := backupDigest(ctx, path)
	if err != nil || before != expectedSHA256 {
		return result, fmt.Errorf("prepared backup is missing or changed")
	}
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return result, err
	}
	result.BackupIntegrity, err = integrityCheck(ctx, db)
	if err == nil {
		result.BackupMigration, err = LatestMigration(ctx, db)
	}
	var violations int
	if err == nil {
		err = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations)
	}
	db.Close()
	if err != nil {
		return result, err
	}
	if result.BackupIntegrity != "ok" || result.BackupMigration != expectedMigration || violations != 0 {
		return result, fmt.Errorf("prepared backup integrity, foreign keys or schema differ")
	}
	after, err := backupDigest(ctx, path)
	if err != nil || after != expectedSHA256 {
		return result, fmt.Errorf("prepared backup changed during verification")
	}
	result.BackupSHA256 = after
	return result, nil
}
