// Package historystorage coordinates explicit local history storage cutovers.
// Plans are device-private recovery metadata, never portable history or recipes
// for arbitrary execution. Normal database opening remains non-migrating.
package historystorage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/memsync"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

const maxPlanBytes = 64 << 10

type GitPlan struct {
	RemoteSHA256 string                         `json:"remote_sha256"`
	Inspection   memsync.CloneStorageInspection `json:"inspection"`
}

type Plan struct {
	Version           int                             `json:"version"`
	ID                string                          `json:"id"`
	Root              string                          `json:"root"`
	ProjectID         string                          `json:"project_id"`
	LineageID         string                          `json:"lineage_id"`
	ConfigSHA256      string                          `json:"config_sha256"`
	Database          database.Status                 `json:"database"`
	DatabaseProjects  int                             `json:"database_projects"`
	SchemaBackup      string                          `json:"schema_backup"`
	ArchiveMode       string                          `json:"archive_mode"`
	Archive           recordsync.ArchiveStoragePaths  `json:"archive"`
	ArchiveInspection *recordsync.ArchiveStorageAudit `json:"archive_inspection,omitempty"`
	Git               *GitPlan                        `json:"git,omitempty"`
}

type Seal struct {
	Plan   Plan   `json:"plan"`
	SHA256 string `json:"sha256"`
}

func (p Plan) Seal() Seal {
	body, _ := json.Marshal(p)
	return Seal{Plan: p, SHA256: recordsync.Hash(string(body))}
}

// canonicalPath resolves existing parent aliases without following the final
// leaf. Missing new parent directories are represented without creating them;
// dangling links and inaccessible existing parents refuse planning.
func canonicalPath(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil || !utf8.ValidString(absolute) || strings.ContainsRune(absolute, 0) {
		return "", fmt.Errorf("valid absolute storage path required")
	}
	parent := filepath.Dir(absolute)
	tail := []string{filepath.Base(absolute)}
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...), nil
		}
		if _, statErr := os.Lstat(parent); !os.IsNotExist(statErr) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", err
		}
		tail = append([]string{filepath.Base(parent)}, tail...)
		parent = next
	}
}

func overlaps(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		if relative, err := filepath.Rel(pair[0], pair[1]); err == nil && (relative == "." || filepath.IsLocal(relative)) {
			return true
		}
	}
	return false
}

func configHash(cfg config.Config) string {
	body, _ := json.Marshal([]any{cfg.Storage.Database, cfg.Backups.Dir, cfg.Sync})
	return recordsync.Hash(string(body))
}

func rolePaths(root string, cfg config.Config, id string) (recordsync.ArchiveStoragePaths, string, error) {
	source, err := canonicalPath(filepath.Join(config.StateDir(root), "history"))
	if err != nil {
		return recordsync.ArchiveStoragePaths{}, "", err
	}
	retained, err := canonicalPath(source + "-retained-" + id)
	if err != nil {
		return recordsync.ArchiveStoragePaths{}, "", err
	}
	schemaBackup, err := canonicalPath(filepath.Join(cfg.Backups.Dir, "before-history-schema-"+id+".db"))
	if err != nil {
		return recordsync.ArchiveStoragePaths{}, "", err
	}
	archiveBackup, err := canonicalPath(filepath.Join(cfg.Backups.Dir, "before-history-archive-"+id+".db"))
	if err != nil {
		return recordsync.ArchiveStoragePaths{}, "", err
	}
	for _, backup := range []string{schemaBackup, archiveBackup} {
		if overlaps(source, backup) || overlaps(retained, backup) || overlaps(cfg.Storage.Database, backup) {
			return recordsync.ArchiveStoragePaths{}, "", fmt.Errorf("storage backup overlaps authoritative data")
		}
	}
	return recordsync.ArchiveStoragePaths{Source: source, Retained: retained, DatabaseBackup: archiveBackup}, schemaBackup, nil
}

// NewPlan performs read-only source inspection, including pending-schema
// identity lookup. Schema changes cover the configured database; archive
// cutover covers this project, and Git cutover preserves every clone ref.
func NewPlan(ctx context.Context, root string) (Plan, error) {
	var p Plan
	root, err := filepath.Abs(root)
	if err != nil {
		return p, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return p, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return p, err
	}
	status, err := database.Inspect(ctx, cfg.Storage.Database)
	if err != nil {
		return p, err
	}
	if status.AppliedMigration < "090_" || (status.State != "pending" && status.State != "current") {
		return p, fmt.Errorf("history storage planning requires a supported schema at or after migration 090")
	}
	db, err := database.OpenForMigrationInspection(ctx, cfg.Storage.Database, status.AppliedMigration)
	if err != nil {
		return p, err
	}
	defer db.Close()
	binding, err := (project.Resolver{DB: db}).Lookup(ctx, root, project.StoreInside(cfg.Storage.Database, root))
	if err != nil {
		return p, err
	}
	identity, err := (portable.Service{DB: db}).Get(ctx, binding.ProjectID)
	if err != nil || identity == nil {
		return p, fmt.Errorf("bound portable project identity required before storage planning")
	}
	id, err := recordsync.NewID()
	if err != nil {
		return p, err
	}
	paths, schemaBackup, err := rolePaths(root, cfg, id)
	if err != nil {
		return p, err
	}
	p = Plan{Version: 1, ID: id, Root: root, ProjectID: binding.ProjectID, LineageID: identity.LineageID, ConfigSHA256: configHash(cfg), Database: status, SchemaBackup: schemaBackup, Archive: paths}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM projects`).Scan(&p.DatabaseProjects); err != nil {
		return Plan{}, err
	}
	archive := recordsync.Archive{DB: db, ProjectID: binding.ProjectID, Lineage: identity.LineageID}
	inspectLegacy := true
	if status.AppliedMigration >= "091_" {
		if initialize, err := archive.InspectStorage(ctx, paths.Source); err == nil {
			inspectLegacy = false
			p.ArchiveMode = "ready"
			if initialize {
				p.ArchiveMode = "initialize"
			} else {
				audit, err := archive.AuditStorage(ctx)
				if err != nil {
					return Plan{}, err
				}
				p.ArchiveInspection = &audit
			}
		}
	}
	if inspectLegacy {
		legacy, err := archive.InspectLegacyStorage(ctx, paths.Source)
		if err != nil {
			return Plan{}, fmt.Errorf("old archive is unavailable or invalid; restore its source or resume the saved cutover plan: %w", err)
		}
		p.ArchiveMode, p.ArchiveInspection = "legacy", &legacy
	}
	if cfg.Sync.Remote != "" {
		clone, err := cfg.Sync.MemoryClone()
		if err != nil {
			return Plan{}, err
		}
		clone, err = canonicalPath(clone)
		if err != nil {
			return Plan{}, err
		}
		if err := checkCloneSeparation(clone, root, cfg, paths, schemaBackup); err != nil {
			return Plan{}, err
		}
		inspection, err := memsync.InspectStorage(ctx, clone, cfg.Sync.Remote, gitAccess(cfg))
		if err != nil {
			return Plan{}, err
		}
		p.Git = &GitPlan{RemoteSHA256: recordsync.Hash(cfg.Sync.Remote), Inspection: inspection}
	}
	return p, nil
}

func gitAccess(cfg config.Config) memsync.Access {
	return memsync.Access{Askpass: cfg.Sync.Askpass, SSHKey: cfg.Sync.SSHKey, NoPrompt: true}
}

func checkCloneSeparation(clone, root string, cfg config.Config, paths recordsync.ArchiveStoragePaths, schemaBackup string) error {
	for _, protected := range []string{root, config.StateDir(root), cfg.Storage.Database, paths.Source, paths.Retained, paths.DatabaseBackup, schemaBackup} {
		if protected == "" {
			continue
		}
		physical, err := canonicalPath(protected)
		if err != nil {
			return err
		}
		if overlaps(clone, physical) {
			return fmt.Errorf("memory clone overlaps protected project storage")
		}
	}
	return nil
}

// SavePlan publishes private recovery metadata exclusively. It cannot dirty an
// authoritative archive, managed clone or one of the planned backup files.
func SavePlan(name string, p Plan) error {
	name, err := canonicalPath(name)
	if err != nil {
		return err
	}
	protected := []string{p.Archive.Source, p.Archive.Retained, p.Archive.DatabaseBackup, p.SchemaBackup, p.SchemaBackup + ".verified", p.Database.Path}
	if p.Git != nil {
		g := p.Git.Inspection.Paths
		protected = append(protected, g.Clone, g.Stage, g.Retained, g.Receipt, g.Receipt+".done")
	}
	for _, path := range protected {
		if overlaps(name, path) {
			return fmt.Errorf("plan file overlaps storage or a recovery destination")
		}
	}
	root, err := recordsync.OpenRoot(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.Marshal(p.Seal())
	if err != nil || len(body) > maxPlanBytes {
		return fmt.Errorf("storage plan exceeds its format bound")
	}
	written, err := recordsync.WriteFile(root, filepath.Base(name), body, false)
	if err == nil && !written {
		return fmt.Errorf("storage plan already exists")
	}
	return err
}

func LoadPlan(name string) (Plan, error) {
	name, err := canonicalPath(name)
	if err != nil {
		return Plan{}, err
	}
	root, err := recordsync.OpenRoot(filepath.Dir(name))
	if err != nil {
		return Plan{}, err
	}
	defer root.Close()
	body, err := recordsync.ReadFile(root, filepath.Base(name), maxPlanBytes)
	if err != nil {
		return Plan{}, err
	}
	var sealed Seal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sealed); err != nil {
		return Plan{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || sealed.Plan.Version != 1 || len(sealed.Plan.ID) != 32 || sealed.Plan.Seal().SHA256 != sealed.SHA256 {
		return Plan{}, fmt.Errorf("invalid storage plan seal")
	}
	if id, err := hex.DecodeString(sealed.Plan.ID); err != nil || len(id) != 16 || strings.ToLower(sealed.Plan.ID) != sealed.Plan.ID {
		return Plan{}, fmt.Errorf("invalid storage plan identity")
	}
	return sealed.Plan, nil
}
