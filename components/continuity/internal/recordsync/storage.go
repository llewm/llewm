package recordsync

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

// ArchiveStoragePaths names the non-overwriting destinations of one explicit
// local cutover. Callers retain these exact paths for preparation and recovery.
type ArchiveStoragePaths struct {
	Source         string `json:"source"`
	Retained       string `json:"retained"`
	DatabaseBackup string `json:"database_backup"`
}

func (p ArchiveStoragePaths) cutover() archiveCutoverPaths {
	return archiveCutoverPaths{Source: p.Source, Retained: p.Retained, DatabaseBackup: p.DatabaseBackup}
}

func (a Archive) storageBinding(ctx context.Context) error {
	if a.DB == nil || a.ProjectID == "" || !lineagePattern.MatchString(a.Lineage) {
		return fmt.Errorf("bound project archive required")
	}
	var lineage string
	if err := a.DB.QueryRowContext(ctx, `SELECT lineage_id FROM project_memory_identities WHERE project_id=?`, a.ProjectID).Scan(&lineage); err != nil {
		return err
	}
	if lineage != a.Lineage {
		return fmt.Errorf("archive lineage differs from project identity")
	}
	return nil
}

// ArchiveStorageAudit describes independently verified durable observations,
// not semantic import, destination delivery or a disposable index receipt.
type ArchiveStorageAudit struct {
	ProjectID       string `json:"project_id"`
	LineageID       string `json:"lineage_id"`
	State           string `json:"state"`
	Envelopes       int64  `json:"envelopes"`
	EnvelopeBytes   int64  `json:"envelope_bytes"`
	Origins         int64  `json:"origins"`
	JournalChanges  int64  `json:"journal_changes"`
	EnvelopesSHA256 string `json:"envelopes_sha256"`
	OriginsSHA256   string `json:"origins_sha256"`
	ReceiptSHA256   string `json:"receipt_sha256,omitempty"`
}

// InspectLegacyStorage verifies the complete old authoritative inventory before
// a schema/storage cutover. It needs only the stable project identity tables,
// so a read-only, exactly version-bound migration inspection can call it.
func (a Archive) InspectLegacyStorage(ctx context.Context, source string) (ArchiveStorageAudit, error) {
	if err := a.storageBinding(ctx); err != nil {
		return ArchiveStorageAudit{}, err
	}
	legacy, err := collectArchiveMigration(ctx, source, a.Lineage)
	if err != nil {
		return ArchiveStorageAudit{}, err
	}
	return ArchiveStorageAudit{
		ProjectID: a.ProjectID, LineageID: a.Lineage, State: "legacy",
		Envelopes: legacy.inventory.Records, EnvelopeBytes: legacy.inventory.Bytes, Origins: legacy.inventory.Origins,
		EnvelopesSHA256: legacy.inventory.EnvelopesSHA256, OriginsSHA256: legacy.inventory.OriginsSHA256,
	}, nil
}

// InspectStorage verifies the runtime route without writes. Its true result
// means an empty fresh-identity proof permits initialization; missing old data
// never returns that result. Ready archives return false with no error.
func (a Archive) InspectStorage(ctx context.Context, legacyDirectory string) (needsInitialization bool, err error) {
	if !filepath.IsAbs(legacyDirectory) {
		return false, fmt.Errorf("absolute legacy archive route required")
	}
	if err := a.storageBinding(ctx); err != nil {
		return false, err
	}
	err = a.store().view(ctx, func(tx *sql.Tx) error {
		var routeErr error
		needsInitialization, routeErr = archiveRouteWithin(ctx, a.store(), tx, legacyDirectory)
		return routeErr
	})
	return needsInitialization, err
}

// CheckStorage verifies ready state and its physical legacy fence using a
// read-only transaction. Unlike normal opening, it never initializes storage.
func (a Archive) CheckStorage(ctx context.Context, legacyDirectory string) error {
	initialize, err := a.InspectStorage(ctx, legacyDirectory)
	if err != nil {
		return err
	}
	if initialize {
		return errArchiveNotReady
	}
	return nil
}

// AuditStorage reads all exact envelope bytes, origin observations and journal
// coverage without consulting derived history indexes or modifying storage.
func (a Archive) AuditStorage(ctx context.Context) (ArchiveStorageAudit, error) {
	var report ArchiveStorageAudit
	if a.DB == nil || a.ProjectID == "" || !lineagePattern.MatchString(a.Lineage) {
		return report, fmt.Errorf("bound project archive required")
	}
	err := a.store().view(ctx, func(tx *sql.Tx) error {
		var lineage string
		if err := tx.QueryRowContext(ctx, `SELECT lineage_id FROM project_memory_identities WHERE project_id=?`, a.ProjectID).Scan(&lineage); err != nil {
			return err
		}
		if lineage != a.Lineage {
			return fmt.Errorf("archive lineage differs from project identity")
		}
		audit, err := a.store().auditWithin(ctx, tx)
		if err != nil {
			return err
		}
		var body, checksum sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT status,substr(migration_json,1,4097),substr(migration_checksum,1,65) FROM history_archive_state WHERE project_id=?`, a.ProjectID).Scan(&report.State, &body, &checksum); err != nil {
			return err
		}
		if body.Valid || checksum.Valid {
			r, err := decodeArchiveCutover(a.store(), body, checksum)
			if err != nil {
				return err
			}
			report.ReceiptSHA256 = Hash(string(r.bytes()))
		}
		report.ProjectID, report.LineageID = a.ProjectID, a.Lineage
		report.Envelopes, report.EnvelopeBytes, report.Origins = audit.Inventory.Records, audit.Inventory.Bytes, audit.Inventory.Origins
		report.JournalChanges = audit.Stamp.Changes
		report.EnvelopesSHA256, report.OriginsSHA256 = audit.Inventory.EnvelopesSHA256, audit.Inventory.OriginsSHA256
		return nil
	})
	return report, err
}

// PrepareStorageMigration requires a caller assertion that every writer is
// stopped. It creates its own verified backup and immutable recovery receipt;
// it neither retires the source nor activates staged observations.
func (a Archive) PrepareStorageMigration(ctx context.Context, paths ArchiveStoragePaths, writersStopped bool) (ArchiveStorageAudit, error) {
	if !writersStopped {
		return ArchiveStorageAudit{}, fmt.Errorf("storage migration requires stopped writers")
	}
	if err := a.storageBinding(ctx); err != nil {
		return ArchiveStorageAudit{}, err
	}
	r, err := prepareArchiveCutover(ctx, a.store(), paths.cutover())
	if err != nil {
		return ArchiveStorageAudit{}, err
	}
	report, err := a.AuditStorage(ctx)
	if err != nil {
		return report, err
	}
	if report.State == "staging" && (report.Envelopes != r.Inventory.Records || report.EnvelopeBytes != r.Inventory.Bytes || report.Origins != r.Inventory.Origins || report.EnvelopesSHA256 != r.Inventory.EnvelopesSHA256 || report.OriginsSHA256 != r.Inventory.OriginsSHA256) {
		return ArchiveStorageAudit{}, fmt.Errorf("staged archive differs from prepared source")
	}
	report.ReceiptSHA256 = Hash(string(r.bytes()))
	return report, nil
}

// CompleteStorageMigration resumes retirement, fencing and activation, retaining
// originals and backups. It verifies the normal runtime gate before returning.
func (a Archive) CompleteStorageMigration(ctx context.Context, paths ArchiveStoragePaths, writersStopped bool) (ArchiveStorageAudit, error) {
	if !writersStopped {
		return ArchiveStorageAudit{}, fmt.Errorf("storage migration requires stopped writers")
	}
	if err := a.storageBinding(ctx); err != nil {
		return ArchiveStorageAudit{}, err
	}
	p, err := paths.cutover().normalized()
	if err != nil {
		return ArchiveStorageAudit{}, err
	}
	if err := completeArchiveCutover(ctx, a.store(), p); err != nil {
		return ArchiveStorageAudit{}, err
	}
	if err := openArchiveStore(ctx, a.store(), p.Source); err != nil {
		return ArchiveStorageAudit{}, err
	}
	return a.AuditStorage(ctx)
}
