package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Status struct {
	Path              string   `json:"path"`
	AppliedMigration  string   `json:"applied_migration"`
	CompiledMigration string   `json:"compiled_migration"`
	PendingMigrations []string `json:"pending_migrations"`
	State             string   `json:"state"`
}

type MigrationRequiredError struct{ Status Status }

func (e *MigrationRequiredError) Error() string {
	return fmt.Sprintf("migration_required: database is at %s and this binary requires %s; run `llewm database migrate --from %s --to %s --backup PATH`", e.Status.AppliedMigration, e.Status.CompiledMigration, e.Status.AppliedMigration, e.Status.CompiledMigration)
}

type MigrateOptions struct {
	From            string
	To              string
	BackupPath      string
	BackupRetention *BackupRetentionOptions
}

type MigrateResult struct {
	Path                 string               `json:"path"`
	From                 string               `json:"from"`
	To                   string               `json:"to"`
	BackupPath           string               `json:"backup_path"`
	BackupMigration      string               `json:"backup_migration"`
	BackupIntegrity      string               `json:"backup_integrity"`
	AppliedMigrations    []string             `json:"applied_migrations"`
	DatabaseIntegrity    string               `json:"database_integrity"`
	ForeignKeyViolations int                  `json:"foreign_key_violations"`
	PrunePlan            *BackupRetentionPlan `json:"prune_plan,omitempty"`
}

// BackupResult is the integrity receipt for a consistent SQLite snapshot.
type BackupResult struct {
	Path            string `json:"path"`
	BackupPath      string `json:"backup_path"`
	BackupMigration string `json:"backup_migration"`
	BackupIntegrity string `json:"backup_integrity"`
}

// NextBackupPath returns a generated name that does not currently exist.
// Backup still refuses overwrites if another process wins the creation race.
func NextBackupPath(dir string, now time.Time) (string, error) {
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	for sequence := 1; ; sequence++ {
		name := "continuity-" + stamp + ".db"
		if sequence > 1 {
			name = fmt.Sprintf("continuity-%s-%d.db", stamp, sequence)
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

// Open opens only a database whose schema already matches this binary. It never
// initializes or migrates storage.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	status, err := Inspect(ctx, path)
	if err != nil {
		return nil, err
	}
	if status.State == "pending" {
		return nil, &MigrationRequiredError{Status: status}
	}
	if status.State != "current" {
		return nil, fmt.Errorf("database schema is %s: applied %s, compiled %s", status.State, status.AppliedMigration, status.CompiledMigration)
	}
	return openReadWrite(ctx, path)
}

// OpenReadOnly checks schema identity and opens storage without write capability.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	status, err := Inspect(ctx, path)
	if err != nil {
		return nil, err
	}
	if status.State == "pending" {
		return nil, &MigrationRequiredError{Status: status}
	}
	if status.State != "current" {
		return nil, fmt.Errorf("database schema is %s: applied %s, compiled %s", status.State, status.AppliedMigration, status.CompiledMigration)
	}
	return openReadOnly(ctx, path)
}

// Initialize creates a new database and applies the embedded schema. Existing
// databases are opened under the same strict, non-migrating contract as Open.
func Initialize(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err == nil {
		return Open(ctx, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := openReadWrite(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db, migrationNames()); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Inspect reads schema identity without creating or changing the database.
func Inspect(ctx context.Context, path string) (Status, error) {
	status := Status{Path: path, PendingMigrations: []string{}}
	compiled, err := CompiledLatestMigration()
	if err != nil {
		return status, err
	}
	status.CompiledMigration = compiled
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status, fmt.Errorf("database is not initialized: %s", path)
		}
		return status, err
	}
	db, err := openReadOnly(ctx, path)
	if err != nil {
		return status, err
	}
	defer db.Close()
	appliedNames, err := appliedMigrationNames(ctx, db)
	if err != nil {
		return status, fmt.Errorf("inspect schema_migrations: %w", err)
	}
	status.AppliedMigration = "none"
	if len(appliedNames) > 0 {
		status.AppliedMigration = appliedNames[len(appliedNames)-1]
	}
	compiledNames := migrationNames()
	switch {
	case equalStrings(appliedNames, compiledNames):
		status.State = "current"
	case prefixStrings(appliedNames, compiledNames):
		status.State = "pending"
		status.PendingMigrations = append(status.PendingMigrations, compiledNames[len(appliedNames):]...)
	case prefixStrings(compiledNames, appliedNames):
		status.State = "ahead"
	default:
		status.State = "inconsistent"
	}
	return status, nil
}

// Backup creates and verifies a consistent, non-overwriting SQLite snapshot.
func Backup(ctx context.Context, path, backupPath string) (BackupResult, error) {
	result := BackupResult{Path: path, BackupPath: backupPath}
	status, err := Inspect(ctx, path)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(backupPath) == "" {
		return result, errors.New("backup path is required")
	}
	absDB, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	absBackup, err := filepath.Abs(backupPath)
	if err != nil {
		return result, err
	}
	if absDB == absBackup {
		return result, errors.New("backup path must differ from database path")
	}
	if _, err := os.Stat(absBackup); err == nil {
		return result, fmt.Errorf("backup already exists: %s", absBackup)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(absBackup), 0o700); err != nil {
		return result, err
	}

	db, err := openReadWrite(ctx, path)
	if err != nil {
		return result, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, absBackup); err != nil {
		return result, fmt.Errorf("create consistent backup: %w", err)
	}
	result.BackupPath = absBackup
	backupDB, err := openReadOnly(ctx, absBackup)
	if err != nil {
		return result, fmt.Errorf("open backup: %w", err)
	}
	result.BackupIntegrity, err = integrityCheck(ctx, backupDB)
	if err == nil {
		result.BackupMigration, err = LatestMigration(ctx, backupDB)
	}
	backupDB.Close()
	if err != nil {
		return result, fmt.Errorf("check backup integrity: %w", err)
	}
	if result.BackupIntegrity != "ok" {
		return result, fmt.Errorf("backup integrity check returned %q", result.BackupIntegrity)
	}
	if result.BackupMigration != status.AppliedMigration {
		return result, fmt.Errorf("backup migration is %s, expected %s", result.BackupMigration, status.AppliedMigration)
	}
	return result, nil
}

// Migrate applies pending migrations only after exact from/to confirmation and a
// verified consistent backup. Ordinary opens never call this function.
func Migrate(ctx context.Context, path string, options MigrateOptions) (MigrateResult, error) {
	result := MigrateResult{Path: path, From: options.From, To: options.To, BackupPath: options.BackupPath}
	status, err := Inspect(ctx, path)
	if err != nil {
		return result, err
	}
	if status.State != "pending" {
		return result, fmt.Errorf("database migration state is %s, not pending", status.State)
	}
	if options.From != status.AppliedMigration || options.To != status.CompiledMigration {
		return result, fmt.Errorf("migration confirmation mismatch: actual from/to is %s -> %s", status.AppliedMigration, status.CompiledMigration)
	}
	backupResult, err := Backup(ctx, path, options.BackupPath)
	if err != nil {
		return result, err
	}
	result.BackupPath = backupResult.BackupPath
	result.BackupMigration = backupResult.BackupMigration
	result.BackupIntegrity = backupResult.BackupIntegrity
	if result.BackupMigration != status.AppliedMigration {
		return result, fmt.Errorf("migration source changed before backup: now %s, confirmed %s", result.BackupMigration, status.AppliedMigration)
	}
	if options.BackupRetention != nil {
		plan, err := PlanBackupRetention(*options.BackupRetention)
		if err != nil {
			return result, fmt.Errorf("plan backup retention: %w", err)
		}
		result.PrunePlan = &plan
	}

	db, err := openReadWrite(ctx, path)
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err := migratePendingAtomic(ctx, db, status.AppliedMigration, status.PendingMigrations); err != nil {
		return result, err
	}
	result.AppliedMigrations = append(result.AppliedMigrations, status.PendingMigrations...)
	result.DatabaseIntegrity, err = integrityCheck(ctx, db)
	if err != nil {
		return result, err
	}
	if result.DatabaseIntegrity != "ok" {
		return result, fmt.Errorf("database integrity check returned %q", result.DatabaseIntegrity)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&result.ForeignKeyViolations); err != nil {
		return result, fmt.Errorf("check foreign keys: %w", err)
	}
	if result.ForeignKeyViolations != 0 {
		return result, fmt.Errorf("foreign key check returned %d violations", result.ForeignKeyViolations)
	}
	return result, nil
}

func migratePendingAtomic(ctx context.Context, db *sql.DB, from string, names []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer reservation before rechecking the confirmed source
	// version, so concurrent explicit migration attempts cannot both proceed.
	if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET applied_at=applied_at WHERE 0`); err != nil {
		return err
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&current)
	if err == sql.ErrNoRows {
		current = "none"
	} else if err != nil {
		return err
	}
	if current != from {
		return fmt.Errorf("migration source changed after backup: now %s, confirmed %s", current, from)
	}
	for _, name := range names {
		if err := applyMigration(ctx, tx, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", 4)
}

func openReadWrite(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, "file:"+filepath.ToSlash(path)+"?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", 4)
}

func open(ctx context.Context, dsn string, maxConnections int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConnections)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB, names []string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL) STRICT`); err != nil {
		return err
	}
	for _, name := range names {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = applyMigration(ctx, tx, name); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func migrationNames() []string {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func integrityCheck(ctx context.Context, db *sql.DB) (string, error) {
	var result string
	err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result)
	return result, err
}

func appliedMigrationNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func prefixStrings(prefix, values []string) bool {
	if len(prefix) > len(values) {
		return false
	}
	for index := range prefix {
		if prefix[index] != values[index] {
			return false
		}
	}
	return true
}

func equalStrings(first, second []string) bool {
	return len(first) == len(second) && prefixStrings(first, second)
}

func LatestMigration(ctx context.Context, db *sql.DB) (string, error) {
	var version string
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		return "none", nil
	}
	return version, err
}

// CompiledLatestMigration returns the newest migration embedded in this binary.
func CompiledLatestMigration() (string, error) {
	names := migrationNames()
	if len(names) == 0 {
		return "none", nil
	}
	return names[len(names)-1], nil
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }
