package historystorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

type schemaBackupReceipt struct {
	Version      int    `json:"version"`
	PlanSHA256   string `json:"plan_sha256"`
	BackupSHA256 string `json:"backup_sha256"`
}

type schemaBackupSeal struct {
	Receipt schemaBackupReceipt `json:"receipt"`
	SHA256  string              `json:"sha256"`
}

func (r schemaBackupReceipt) sealed() []byte {
	body, _ := json.Marshal(r)
	seal, _ := json.Marshal(schemaBackupSeal{Receipt: r, SHA256: recordsync.Hash(string(body))})
	return seal
}

func readSchemaBackupReceipt(p Plan) (schemaBackupReceipt, bool, error) {
	var empty schemaBackupReceipt
	root, err := recordsync.OpenRoot(filepath.Dir(p.SchemaBackup))
	if errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	defer root.Close()
	body, err := recordsync.ReadFile(root, filepath.Base(p.SchemaBackup)+".verified", 1024)
	if errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	var seal schemaBackupSeal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&seal); err != nil {
		return empty, false, err
	}
	r := seal.Receipt
	if r.Version != 1 || r.PlanSHA256 != p.Seal().SHA256 || !bytes.Equal(body, r.sealed()) {
		return empty, false, fmt.Errorf("pre-schema backup receipt differs from this plan")
	}
	return r, true, nil
}

func verifySchemaBackupReceipt(ctx context.Context, p Plan, r schemaBackupReceipt) error {
	if _, err := database.VerifyBackup(ctx, p.SchemaBackup, p.Database.AppliedMigration, r.BackupSHA256); err != nil {
		return err
	}
	db, err := database.OpenForMigrationInspection(ctx, p.SchemaBackup, p.Database.AppliedMigration)
	if err != nil {
		return err
	}
	defer db.Close()
	var identities int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM project_memory_identities WHERE project_id=? AND lineage_id=?`, p.ProjectID, p.LineageID).Scan(&identities); err != nil {
		return err
	}
	if identities != 1 {
		return fmt.Errorf("pre-schema backup project identity differs")
	}
	return nil
}

// The durable receipt is published before any schema change. An unregistered
// backup left before receipt publication stays untouched; use a fresh plan.
func prepareSchemaBackup(ctx context.Context, p Plan) (schemaBackupReceipt, error) {
	r, found, err := readSchemaBackupReceipt(p)
	if err != nil {
		return r, err
	}
	if found {
		return r, verifySchemaBackupReceipt(ctx, p, r)
	}
	if _, err := os.Lstat(p.SchemaBackup); !errors.Is(err, os.ErrNotExist) {
		return r, fmt.Errorf("pre-schema backup exists without a matching receipt; retain it and create a fresh plan")
	}
	backup, err := database.Backup(ctx, p.Database.Path, p.SchemaBackup)
	if err != nil {
		return r, err
	}
	r = schemaBackupReceipt{Version: 1, PlanSHA256: p.Seal().SHA256, BackupSHA256: backup.BackupSHA256}
	if err := verifySchemaBackupReceipt(ctx, p, r); err != nil {
		return r, err
	}
	root, err := recordsync.OpenRoot(filepath.Dir(p.SchemaBackup))
	if err != nil {
		return r, err
	}
	defer root.Close()
	_, err = recordsync.WriteFile(root, filepath.Base(p.SchemaBackup)+".verified", r.sealed(), false)
	return r, err
}
