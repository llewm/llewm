package historystorage

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

type Result struct {
	PlanSHA256 string                         `json:"plan_sha256,omitempty"`
	State      string                         `json:"state"`
	Database   *database.MigrateResult        `json:"database,omitempty"`
	Archive    recordsync.ArchiveStorageAudit `json:"archive"`
	Git        *memsync.CloneStorageAudit     `json:"git,omitempty"`
}

// validateScope checks current configuration and project identity before any
// mutation. Source-frontier counts are advisory; paths, policy, lineage and
// exact schema transition remain bound to the reviewed plan.
func validateScope(ctx context.Context, root string, p Plan) (config.Config, database.Status, error) {
	var cfg config.Config
	var status database.Status
	id, err := hex.DecodeString(p.ID)
	if err != nil || len(id) != 16 || len(p.ID) != 32 || strings.ToLower(p.ID) != p.ID || p.Version != 1 {
		return cfg, status, fmt.Errorf("invalid storage plan identity")
	}
	root, err = filepath.Abs(root)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil || root != p.Root {
		return cfg, status, fmt.Errorf("storage plan belongs to a different project root")
	}
	cfg, err = config.Load(root)
	if err != nil {
		return cfg, status, err
	}
	paths, schemaBackup, err := rolePaths(root, cfg, p.ID)
	if err != nil {
		return cfg, status, err
	}
	if p.ConfigSHA256 != configHash(cfg) || p.Database.Path != cfg.Storage.Database || paths != p.Archive || schemaBackup != p.SchemaBackup {
		return cfg, status, fmt.Errorf("storage plan paths or configuration changed")
	}
	if p.ArchiveMode != "legacy" && p.ArchiveMode != "ready" && p.ArchiveMode != "initialize" {
		return cfg, status, fmt.Errorf("invalid planned archive mode")
	}
	status, err = database.Inspect(ctx, cfg.Storage.Database)
	if err != nil {
		return cfg, status, err
	}
	if status.CompiledMigration != p.Database.CompiledMigration || (status.AppliedMigration != p.Database.AppliedMigration && status.AppliedMigration != p.Database.CompiledMigration) || (status.State != "current" && status.State != "pending") || p.Database.AppliedMigration < "090_" {
		return cfg, status, fmt.Errorf("storage plan schema transition changed")
	}
	if p.Database.AppliedMigration < "091_" && p.ArchiveMode != "legacy" {
		return cfg, status, fmt.Errorf("old identities require authoritative archive migration")
	}
	db, err := database.OpenForMigrationInspection(ctx, cfg.Storage.Database, status.AppliedMigration)
	if err != nil {
		return cfg, status, err
	}
	defer db.Close()
	binding, err := (project.Resolver{DB: db}).Lookup(ctx, root, project.StoreInside(cfg.Storage.Database, root))
	if err != nil || binding.ProjectID != p.ProjectID {
		return cfg, status, fmt.Errorf("storage plan project binding changed")
	}
	identity, err := (portable.Service{DB: db}).Get(ctx, p.ProjectID)
	if err != nil || identity == nil || identity.LineageID != p.LineageID {
		return cfg, status, fmt.Errorf("storage plan lineage changed")
	}
	if cfg.Sync.Remote == "" {
		if p.Git != nil {
			return cfg, status, fmt.Errorf("storage plan Git route changed")
		}
	} else {
		clone, err := cfg.Sync.MemoryClone()
		if err != nil {
			return cfg, status, err
		}
		clone, err = canonicalPath(clone)
		if err != nil {
			return cfg, status, err
		}
		gitPaths, err := memsync.StoragePaths(clone)
		if err != nil {
			return cfg, status, err
		}
		if p.Git == nil || p.Git.RemoteSHA256 != recordsync.Hash(cfg.Sync.Remote) || gitPaths != p.Git.Inspection.Paths {
			return cfg, status, fmt.Errorf("storage plan Git destination changed")
		}
		if err := checkCloneSeparation(clone, root, cfg, paths, schemaBackup); err != nil {
			return cfg, status, err
		}
	}
	return cfg, status, nil
}

func verifySchemaBackup(ctx context.Context, p Plan) error {
	if p.Database.AppliedMigration == p.Database.CompiledMigration {
		return nil
	}
	r, found, err := readSchemaBackupReceipt(p)
	if err != nil || !found {
		return fmt.Errorf("verified pre-schema backup receipt required for resumption")
	}
	return verifySchemaBackupReceipt(ctx, p, r)
}

// Prepare requires every writer to have been drained by the caller. Git is
// verified/staged before any schema change, so dirty source recovery can still
// use the old compatible runtime. Only explicit database.Migrate raises schema.
func Prepare(ctx context.Context, root string, p Plan, writersStopped bool) (Result, error) {
	result := Result{PlanSHA256: p.Seal().SHA256, State: "preparing"}
	if !writersStopped {
		return result, fmt.Errorf("storage preparation requires --writers-stopped after draining every writer")
	}
	cfg, status, err := validateScope(ctx, root, p)
	if err != nil {
		return result, err
	}
	if p.Git != nil {
		inspection, err := memsync.InspectStorage(ctx, p.Git.Inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg))
		if err != nil {
			return result, err
		}
		if inspection.State != "bare" {
			if _, err := memsync.PrepareStorageMigration(ctx, inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg), true); err != nil {
				return result, err
			}
		}
	}
	if status.AppliedMigration != p.Database.CompiledMigration {
		// Verify old source bytes before making the old writer incompatible.
		db, err := database.OpenForMigrationInspection(ctx, cfg.Storage.Database, status.AppliedMigration)
		if err != nil {
			return result, err
		}
		_, sourceErr := (recordsync.Archive{DB: db, ProjectID: p.ProjectID, Lineage: p.LineageID}).InspectLegacyStorage(ctx, p.Archive.Source)
		db.Close()
		if sourceErr != nil {
			return result, sourceErr
		}
		receipt, err := prepareSchemaBackup(ctx, p)
		if err != nil {
			return result, err
		}
		migration, err := database.Migrate(ctx, cfg.Storage.Database, database.MigrateOptions{From: p.Database.AppliedMigration, To: p.Database.CompiledMigration, BackupPath: p.SchemaBackup, VerifiedBackupSHA256: receipt.BackupSHA256})
		if err != nil {
			return result, err
		}
		result.Database = &migration
	} else if err := verifySchemaBackup(ctx, p); err != nil {
		return result, err
	}
	if p.ArchiveMode == "legacy" {
		if err := os.MkdirAll(filepath.Dir(p.Archive.DatabaseBackup), 0700); err != nil {
			return result, err
		}
	}
	db, err := database.Open(ctx, cfg.Storage.Database)
	if err != nil {
		return result, err
	}
	defer db.Close()
	archive := recordsync.Archive{DB: db, ProjectID: p.ProjectID, Lineage: p.LineageID}
	if p.ArchiveMode == "legacy" {
		result.Archive, err = archive.PrepareStorageMigration(ctx, p.Archive, true)
	} else {
		if p.ArchiveMode == "initialize" {
			_, err = recordsync.OpenArchive(ctx, db, p.ProjectID, p.LineageID, p.Archive.Source)
		} else {
			err = archive.CheckStorage(ctx, p.Archive.Source)
		}
		if err == nil {
			result.Archive, err = archive.AuditStorage(ctx)
		}
	}
	if err != nil {
		return result, err
	}
	result.State = "prepared"
	return result, nil
}

// Complete activates only verified prepared storage, retaining all sources and
// backups. Full audits inspect actual bytes; no remote/network action occurs.
func Complete(ctx context.Context, root string, p Plan, writersStopped bool) (Result, error) {
	result := Result{PlanSHA256: p.Seal().SHA256, State: "completing"}
	if !writersStopped {
		return result, fmt.Errorf("storage completion requires --writers-stopped after draining every writer")
	}
	cfg, status, err := validateScope(ctx, root, p)
	if err != nil {
		return result, err
	}
	if status.AppliedMigration != p.Database.CompiledMigration {
		return result, fmt.Errorf("prepare storage before completing its cutover")
	}
	if err := verifySchemaBackup(ctx, p); err != nil {
		return result, err
	}
	// Verify/stage Git before retiring either authoritative source. This is
	// idempotent and can resume preparation after an interrupted prior step.
	if p.Git != nil {
		inspection, err := memsync.InspectStorage(ctx, p.Git.Inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg))
		if err != nil {
			return result, err
		}
		if inspection.State != "bare" {
			if _, err := memsync.PrepareStorageMigration(ctx, inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg), true); err != nil {
				return result, err
			}
		}
	}
	db, err := database.Open(ctx, cfg.Storage.Database)
	if err != nil {
		return result, err
	}
	defer db.Close()
	archive := recordsync.Archive{DB: db, ProjectID: p.ProjectID, Lineage: p.LineageID}
	if p.ArchiveMode == "legacy" {
		result.Archive, err = archive.CompleteStorageMigration(ctx, p.Archive, true)
	} else if err = archive.CheckStorage(ctx, p.Archive.Source); err == nil {
		result.Archive, err = archive.AuditStorage(ctx)
	}
	if err != nil {
		return result, err
	}
	if p.Git != nil {
		inspection, err := memsync.InspectStorage(ctx, p.Git.Inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg))
		if err != nil {
			return result, err
		}
		if inspection.State != "bare" {
			if err := memsync.CompleteStorageMigration(ctx, inspection.Paths.Clone, cfg.Sync.Remote, gitAccess(cfg), true); err != nil {
				return result, err
			}
		}
		audit, err := memsync.AuditStorage(ctx, inspection.Paths.Clone, cfg.Sync.Remote, p.LineageID, gitAccess(cfg))
		if err != nil {
			return result, err
		}
		result.Git = &audit
	}
	result.State = "ready"
	return result, nil
}

// Audit verifies local durable archive data and the configured primary managed
// Git clone without modifying storage or contacting a remote. Folder provider
// delivery and semantic import are outside this local storage audit.
func Audit(ctx context.Context, root string) (Result, error) {
	result := Result{State: "auditing"}
	root, err := filepath.Abs(root)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return result, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return result, err
	}
	db, err := database.OpenReadOnly(ctx, cfg.Storage.Database)
	if err != nil {
		return result, err
	}
	defer db.Close()
	binding, err := (project.Resolver{DB: db}).Lookup(ctx, root, project.StoreInside(cfg.Storage.Database, root))
	if err != nil {
		return result, err
	}
	identity, err := (portable.Service{DB: db}).Get(ctx, binding.ProjectID)
	if err != nil || identity == nil {
		return result, fmt.Errorf("bound portable identity required for storage audit")
	}
	archive := recordsync.Archive{DB: db, ProjectID: binding.ProjectID, Lineage: identity.LineageID}
	if err := archive.CheckStorage(ctx, filepath.Join(config.StateDir(root), "history")); err != nil {
		return result, err
	}
	result.Archive, err = archive.AuditStorage(ctx)
	if err != nil {
		return result, err
	}
	if cfg.Sync.Remote != "" {
		clone, err := cfg.Sync.MemoryClone()
		if err != nil {
			return result, err
		}
		physical, err := canonicalPath(clone)
		if err != nil {
			return result, err
		}
		if err := checkCloneSeparation(physical, root, cfg, recordsync.ArchiveStoragePaths{Source: filepath.Join(config.StateDir(root), "history")}, ""); err != nil {
			return result, err
		}
		git, err := memsync.AuditStorage(ctx, clone, cfg.Sync.Remote, identity.LineageID, gitAccess(cfg))
		if err != nil {
			return result, err
		}
		result.Git = &git
	}
	result.State = "verified"
	return result, nil
}
