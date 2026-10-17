// Package layout implements an explicit, backup-gated pre-release state move.
// It does not discover or stop writers and never edits a memory repository.
package layout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/llewm/llewm/components/continuity/internal/adoption"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

const Manifest = "layout_version = 1\n"

type Plan struct {
	Root             string `json:"root"`
	Source           string `json:"source"`
	Destination      string `json:"destination"`
	Database         string `json:"database"`
	DatabaseRelative string `json:"database_relative"`
	ConfigSHA256     string `json:"config_sha256"`
	Workspace        bool   `json:"workspace"`
}
type Receipt struct {
	DatabaseIntegrity    string                `json:"database_integrity"`
	ForeignKeyViolations int                   `json:"foreign_key_violations"`
	Version              int                   `json:"version"`
	Plan                 Plan                  `json:"plan"`
	Backup               string                `json:"backup"`
	Snapshot             database.BackupResult `json:"snapshot"`
	Stage                string                `json:"stage"`
	OriginalFiles        map[string]string     `json:"original_files"`
	MigratedFiles        map[string]string     `json:"migrated_files"`
	ManifestSHA256       string                `json:"manifest_sha256"`
	WorkspaceSHA256      string                `json:"workspace_sha256,omitempty"`
}

func exists(p string) bool { _, e := os.Lstat(p); return e == nil }
func noSymlinks(p string) error {
	p, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for current := p; ; current = filepath.Dir(current) {
		info, e := os.Lstat(current)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink refused: %s", current)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func relative(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(base), path)
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", fmt.Errorf("path is outside legacy state: %s", path)
	}
	return rel, nil
}
func Preview(ctx context.Context, root string) (Plan, error) {
	p := Plan{Root: root, Source: filepath.Join(root, ".continuity"), Destination: filepath.Join(root, ".llewm", "continuity")}
	if os.Getenv("CONTINUITY_HOME") != "" {
		return p, errors.New("layout migration requires default project storage; remove CONTINUITY_HOME only after reviewing its target")
	}
	for _, path := range []string{p.Source, filepath.Join(root, ".llewm")} {
		if err := noSymlinks(path); err != nil {
			return p, err
		}
	}
	if !exists(p.Source) {
		return p, errors.New("no legacy .continuity directory; nothing to migrate")
	}
	for _, path := range []string{p.Destination, filepath.Join(root, ".llewm", "project.toml"), filepath.Join(root, ".llewm", "workspace.toml")} {
		if exists(path) {
			return p, fmt.Errorf("layout destination collision: %s", path)
		}
	}
	if _, err := tree(p.Source); err != nil {
		return p, err
	}
	cfgPath := filepath.Join(p.Source, config.FileName)
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return p, err
	}
	p.ConfigSHA256 = adoption.Digest(body)
	cfg := config.Default(root)
	// Defaults refer to the new layout; legacy absent keys mean legacy defaults.
	cfg.Storage.Database = filepath.Join(p.Source, "continuity.db")
	cfg.Backups.Dir = filepath.Join(p.Source, "backups")
	if _, err = toml.Decode(string(body), &cfg); err != nil {
		return p, err
	}
	if p.DatabaseRelative, err = relative(p.Source, cfg.Storage.Database); err != nil {
		return p, err
	}
	p.Database = filepath.Join(p.Source, p.DatabaseRelative)
	if _, err = relative(p.Source, cfg.Backups.Dir); err != nil {
		return p, err
	}
	if cfg.Sync.Clone != "" {
		clone := cfg.Sync.Clone
		if !filepath.IsAbs(clone) {
			clone = filepath.Join(root, clone)
		}
		if rel, e := filepath.Rel(p.Source, clone); e == nil && (rel == "." || filepath.IsLocal(rel)) {
			return p, errors.New("memory clone is inside legacy state; relocate it through supported History-sync procedures before layout migration")
		}
	}
	if _, err = tree(p.Source); err != nil {
		return p, err
	}
	status, err := database.Inspect(ctx, p.Database)
	if err != nil {
		return p, err
	}
	if status.State != "current" {
		return p, errors.New("legacy database must be current before layout migration; use its matching binary and backup-gated schema migration first")
	}
	p.Workspace = exists(filepath.Join(p.Source, "workspace.toml"))
	return p, nil
}

// tree inventories exact regular bytes, including SQLite sidecars. It refuses
// symlinks and special files rather than reaching outside the migration scope.
func tree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular state entry refused: %s", path)
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return out, err
}
func writeJSON(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, b, 0600)
}
func Migrate(ctx context.Context, root, backup string, stopped bool) (Receipt, error) {
	return migrate(ctx, root, backup, stopped, os.Rename)
}
func migrate(ctx context.Context, root, backup string, stopped bool, rename func(string, string) error) (receipt Receipt, err error) {
	p, err := Preview(ctx, root)
	if err != nil {
		return receipt, err
	}
	receipt.Plan = p
	receipt.Version = 1
	if !stopped {
		return receipt, errors.New("stop/drain all old CLI, hook, MCP and sync writers first, then attest --writers-stopped; this tool cannot prove they stopped")
	}
	if strings.TrimSpace(backup) == "" {
		return receipt, errors.New("backup directory is required")
	}
	backup, err = filepath.Abs(backup)
	if err != nil {
		return receipt, err
	}
	if backup == root || !filepath.IsAbs(backup) {
		return receipt, errors.New("backup must be a new directory outside both state locations")
	}
	if strings.TrimSpace(backup) == "" {
		return receipt, errors.New("backup directory is required")
	}
	for _, base := range []string{p.Source, filepath.Join(root, ".llewm")} {
		if rel, e := filepath.Rel(base, backup); e == nil && (rel == "." || filepath.IsLocal(rel)) {
			return receipt, errors.New("backup must be outside both state locations")
		}
	}
	if err = noSymlinks(backup); err != nil {
		return receipt, err
	}
	if exists(backup) {
		return receipt, errors.New("backup directory already exists")
	}
	if err = os.MkdirAll(filepath.Join(root, ".llewm"), 0700); err != nil {
		return receipt, err
	}
	lockPath := filepath.Join(root, ".llewm", "layout.lock")
	lock, e := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return receipt, fmt.Errorf("layout operation pending; inspect layout.lock and use restore-layout for crash recovery: %w", e)
	}
	_, e = lock.WriteString(backup)
	closeErr := lock.Close()
	if e != nil {
		return receipt, e
	}
	if closeErr != nil {
		return receipt, closeErr
	}
	keepLock := false
	defer func() {
		if !keepLock {
			os.Remove(lockPath)
		}
	}()
	if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return receipt, err
	}
	if err = os.Mkdir(backup, 0700); err != nil {
		return receipt, err
	}
	receipt.Backup = backup
	// Drain SQLite WAL after the caller's writer-stop attestation. A busy reader
	// refuses the cutover; idle old processes are still the caller's responsibility.
	db, err := database.Open(ctx, p.Database)
	if err != nil {
		return receipt, err
	}
	var busy, log, checkpointed int
	err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	db.Close()
	if err != nil {
		return receipt, err
	}
	if busy != 0 {
		return receipt, errors.New("SQLite WAL is busy; drain remaining writers/readers")
	}
	receipt.Snapshot, err = database.Backup(ctx, p.Database, filepath.Join(backup, "snapshot.db"))
	if err != nil {
		return receipt, err
	}
	receipt.OriginalFiles, err = tree(p.Source)
	if err != nil {
		return receipt, err
	}
	stage, err := os.MkdirTemp(filepath.Join(root, ".llewm"), ".layout-stage-*")
	if err != nil {
		return receipt, err
	}
	receipt.Stage = stage
	defer func() {
		if err != nil && !exists(filepath.Join(backup, "source")) {
			os.RemoveAll(stage)
		}
	}()
	for rel := range receipt.OriginalFiles {
		if rel == filepath.ToSlash(p.DatabaseRelative) || rel == filepath.ToSlash(p.DatabaseRelative)+"-wal" || rel == filepath.ToSlash(p.DatabaseRelative)+"-shm" {
			continue
		}
		if err = copyFile(filepath.Join(p.Source, filepath.FromSlash(rel)), filepath.Join(stage, filepath.FromSlash(rel))); err != nil {
			return receipt, err
		}
	}
	if err = copyFile(receipt.Snapshot.BackupPath, filepath.Join(stage, p.DatabaseRelative)); err != nil {
		return receipt, err
	}
	if err = rewriteConfig(p, stage); err != nil {
		return receipt, err
	}
	var workspace []byte
	if p.Workspace {
		workspace, err = os.ReadFile(filepath.Join(stage, "workspace.toml"))
		if err != nil {
			return receipt, err
		}
		receipt.WorkspaceSHA256 = adoption.Digest(workspace)
		if err = os.Remove(filepath.Join(stage, "workspace.toml")); err != nil {
			return receipt, err
		}
	}
	stagedDB := filepath.Join(stage, p.DatabaseRelative)
	db, err = database.Open(ctx, stagedDB)
	if err != nil {
		return receipt, err
	}
	err = database.RaiseWriterFloor(ctx, db, buildinfo.WriterAPIVersion)
	if err == nil {
		var integrity string
		err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
		receipt.DatabaseIntegrity = integrity
		if err == nil && integrity != "ok" {
			err = fmt.Errorf("staged database integrity: %s", integrity)
		}
	}
	if err == nil {
		fk, e := db.QueryContext(ctx, "PRAGMA foreign_key_check")
		if e != nil {
			err = e
		} else {
			if fk.Next() {
				receipt.ForeignKeyViolations++
				err = errors.New("staged database has foreign-key violations")
			}
			if e = fk.Err(); err == nil {
				err = e
			}
			fk.Close()
		}
	}
	db.Close()
	if err != nil {
		return receipt, err
	}
	receipt.MigratedFiles, err = tree(stage)
	if err != nil {
		return receipt, err
	}
	receipt.ManifestSHA256 = adoption.Digest([]byte(Manifest))
	if err = writeJSON(filepath.Join(backup, "receipt.json"), receipt); err != nil {
		return receipt, err
	}
	// Nothing is destroyed. On an ordinary failure, restore the exact source.
	retired := false
	installed := false
	manifest := false
	ws := false
	defer func() {
		if err == nil {
			return
		}
		var recovery []error
		if ws {
			recovery = append(recovery, os.Remove(filepath.Join(root, ".llewm", "workspace.toml")))
		}
		if manifest {
			recovery = append(recovery, os.Remove(filepath.Join(root, ".llewm", "project.toml")))
		}
		if installed {
			recovery = append(recovery, os.Rename(p.Destination, stage))
		}
		if retired {
			recovery = append(recovery, os.Rename(filepath.Join(backup, "source"), p.Source))
		}
		for _, e := range recovery {
			if e != nil {
				keepLock = true
			}
		}
		err = errors.Join(append([]error{err}, recovery...)...)
	}()
	if err = matches(p.Source, receipt.OriginalFiles, p.DatabaseRelative); err != nil {
		return receipt, fmt.Errorf("legacy state changed during staging: %w", err)
	}
	if err = rename(p.Source, filepath.Join(backup, "source")); err != nil {
		return receipt, err
	}
	retired = true
	if err = rename(stage, p.Destination); err != nil {
		return receipt, err
	}
	installed = true
	manifest = true
	if err = os.WriteFile(filepath.Join(root, ".llewm", "project.toml"), []byte(Manifest), 0600); err != nil {
		return receipt, err
	}
	manifest = true
	if p.Workspace {
		ws = true
		if err = os.WriteFile(filepath.Join(root, ".llewm", "workspace.toml"), workspace, 0600); err != nil {
			return receipt, err
		}
		ws = true
	}
	return receipt, nil
}
func copyFile(from, to string) error {
	input, err := os.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	if err = os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	output, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	closeErr := output.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func rewriteConfig(p Plan, stage string) error {
	var values map[string]any
	if _, err := toml.DecodeFile(filepath.Join(stage, config.FileName), &values); err != nil {
		return err
	}
	storage, ok := values["storage"].(map[string]any)
	if !ok {
		storage = map[string]any{}
		values["storage"] = storage
	}
	storage["database"] = filepath.ToSlash(filepath.Join(".llewm", "continuity", p.DatabaseRelative))
	backups, ok := values["backups"].(map[string]any)
	if !ok {
		backups = map[string]any{}
		values["backups"] = backups
	}
	if value, ok := backups["dir"].(string); ok {
		rel, err := relative(p.Source, value)
		if err != nil {
			return err
		}
		backups["dir"] = filepath.ToSlash(filepath.Join(".llewm", "continuity", rel))
	} else {
		backups["dir"] = ".llewm/continuity/backups"
	}
	if pins, ok := backups["pinned"].([]any); ok {
		for i, item := range pins {
			if pin, ok := item.(string); ok && filepath.IsAbs(pin) {
				if rel, e := filepath.Rel(p.Source, pin); e == nil && filepath.IsLocal(rel) {
					pins[i] = filepath.Join(p.Destination, rel)
				}
			}
		}
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(values); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, config.FileName), b.Bytes(), 0600)
}

// Restore is deliberately conservative: new writes or edited config/prose state
// make automatic rollback unsafe. It handles a prepared or interrupted cutover.
func Restore(root, backup string, stopped bool) error {
	if !stopped {
		return errors.New("restore requires --writers-stopped")
	}
	if err := noSymlinks(filepath.Join(backup, "receipt.json")); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(backup, "receipt.json"))
	if err != nil {
		return err
	}
	var r Receipt
	if err = json.Unmarshal(body, &r); err != nil {
		return err
	}
	abs, err := filepath.Abs(backup)
	if err != nil {
		return err
	}
	if r.Version != 1 || r.Plan.Root != root || r.Backup != abs || r.Plan.Source != filepath.Join(root, ".continuity") || r.Plan.Destination != filepath.Join(root, ".llewm", "continuity") || filepath.Dir(r.Stage) != filepath.Join(root, ".llewm") || !strings.HasPrefix(filepath.Base(r.Stage), ".layout-stage-") {
		return errors.New("backup receipt does not match this project/layout")
	}
	for _, path := range []string{r.Plan.Source, r.Plan.Destination, r.Stage, filepath.Join(root, ".llewm")} {
		if err = noSymlinks(path); err != nil {
			return err
		}
	}
	lockPath := filepath.Join(root, ".llewm", "layout.lock")
	if b, e := os.ReadFile(lockPath); e == nil {
		if string(b) != abs {
			return errors.New("different layout operation owns lock")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	source := filepath.Join(abs, "source")
	if exists(r.Plan.Source) {
		if exists(source) || exists(r.Plan.Destination) {
			return errors.New("ambiguous source/destination; preserve all stores and inspect manually")
		}
		if err = matches(r.Plan.Source, r.OriginalFiles, r.Plan.DatabaseRelative); err != nil {
			return err
		}
	} else {
		if err = matches(source, r.OriginalFiles, r.Plan.DatabaseRelative); err != nil {
			return fmt.Errorf("original source changed: %w", err)
		}
		if exists(r.Plan.Destination) {
			if err = matches(r.Plan.Destination, r.MigratedFiles, r.Plan.DatabaseRelative); err != nil {
				return fmt.Errorf("new state changed; retain both stores and reconcile new work before rollback: %w", err)
			}
		} else if err = matches(r.Stage, r.MigratedFiles, r.Plan.DatabaseRelative); err != nil {
			return err
		}
	}
	for file, hash := range map[string]string{"project.toml": r.ManifestSHA256, "workspace.toml": r.WorkspaceSHA256} {
		path := filepath.Join(root, ".llewm", file)
		if exists(path) {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if hash == "" || adoption.Digest(b) != hash {
				return fmt.Errorf("layout file changed: %s", path)
			}
		}
	}
	if exists(r.Plan.Destination) {
		if exists(r.Stage) {
			return errors.New("migration staging collision")
		}
		if err = os.Rename(r.Plan.Destination, r.Stage); err != nil {
			return err
		}
	}
	if !exists(r.Plan.Source) {
		if err = os.Rename(source, r.Plan.Source); err != nil {
			return err
		}
	}
	for _, file := range []string{"project.toml", "workspace.toml"} {
		path := filepath.Join(root, ".llewm", file)
		if exists(path) {
			if err = os.Remove(path); err != nil {
				return err
			}
		}
	}
	if err = os.RemoveAll(r.Stage); err != nil {
		return err
	}
	if exists(lockPath) {
		if err = os.Remove(lockPath); err != nil {
			return err
		}
	}
	return nil
}
func matches(dir string, expected map[string]string, dbRel string) error {
	dbRel = filepath.ToSlash(dbRel)
	got, err := tree(dir)
	if err != nil {
		return err
	}
	// SQLite can leave empty WAL and SHM files on a read-only inspection. A
	// nonempty WAL is always a refusal, never discarded as a harmless sidecar.
	for path := range got {
		if path == dbRel+"-shm" {
			delete(got, path)
		}
		if path == dbRel+"-wal" {
			info, e := os.Stat(filepath.Join(dir, filepath.FromSlash(path)))
			if e != nil {
				return e
			}
			if info.Size() != 0 {
				return errors.New("nonempty SQLite WAL; drain/checkpoint and review before rollback")
			}
			delete(got, path)
		}
	}
	copyExpected := map[string]string{}
	for p, h := range expected {
		if p != dbRel+"-shm" && p != dbRel+"-wal" {
			copyExpected[p] = h
		}
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(copyExpected)
	if !bytes.Equal(a, b) {
		return errors.New("state files differ from migration receipt")
	}
	return nil
}

// EnsureManifest is called by explicit project init, never implicitly by reads.
func EnsureManifest(root string) error {
	if os.Getenv("CONTINUITY_HOME") != "" {
		return nil
	}
	path := filepath.Join(root, ".llewm", "project.toml")
	if err := noSymlinks(path); err != nil {
		return err
	}
	if exists(path) {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(b) != Manifest {
			return errors.New("unknown .llewm project layout")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(Manifest)
	e := f.Close()
	if err != nil {
		return err
	}
	return e
}
