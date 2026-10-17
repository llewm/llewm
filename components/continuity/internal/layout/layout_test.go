package layout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
)

func legacyFixture(t *testing.T) (context.Context, string, string) {
	t.Helper()
	t.Setenv("CONTINUITY_HOME", "")
	ctx := context.Background()
	root := t.TempDir()
	old := filepath.Join(root, ".continuity")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	body := "schema_version=1\nmode='full'\n[project]\nname='Trial'\n[storage]\ndatabase='.continuity/continuity.db'\n[backups]\ndir='.continuity/backups'\n[custom]\nkeep='untouched'\n"
	body = strings.Replace(body, "dir='.continuity/backups'", fmt.Sprintf("dir='.continuity/backups'\npinned=[%q,'keep.db']", filepath.Join(old, "backups", "pinned.db")), 1)
	if err := os.WriteFile(filepath.Join(old, "config.toml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Initialize(ctx, filepath.Join(old, "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "CREATE TABLE trial_learning (id TEXT PRIMARY KEY, body TEXT); INSERT INTO trial_learning VALUES ('stable-identity','Keep new knowledge')"); err != nil {
		t.Fatal(err)
	}
	if err = database.SetWriterFloor(ctx, db, 121); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.WriteFile(filepath.Join(old, "workspace.toml"), []byte("schema_version=1\nmode='unified'\nname='trial'\n[[members]]\npath='member'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return ctx, root, filepath.Join(t.TempDir(), "backup")
}
func TestLayoutMoveAndRollbackPreserveState(t *testing.T) {
	ctx, root, backup := legacyFixture(t)
	if _, err := config.Load(root); err == nil || !strings.Contains(err.Error(), "migrate-layout") {
		t.Fatalf("legacy did not fail closed: %v", err)
	}
	if _, err := Migrate(ctx, root, backup, false); err == nil {
		t.Fatal("migration without writer stop")
	}
	r, err := Migrate(ctx, root, backup, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot.BackupIntegrity != "ok" || r.ManifestSHA256 == "" || r.DatabaseIntegrity != "ok" || r.ForeignKeyViolations != 0 {
		t.Fatalf("missing receipt: %+v", r)
	}
	if exists(filepath.Join(root, ".continuity")) {
		t.Fatal("legacy fallback left behind")
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Database != filepath.Join(root, ".llewm", "continuity", "continuity.db") {
		t.Fatal(cfg.Storage.Database)
	}
	if len(cfg.Backups.Pinned) != 2 || cfg.Backups.Pinned[0] != filepath.Join(root, ".llewm", "continuity", "backups", "pinned.db") || cfg.Backups.Pinned[1] != "keep.db" {
		t.Fatalf("backup pins changed protection: %+v", cfg.Backups.Pinned)
	}
	db, err := database.OpenReadOnly(ctx, cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	var id, body string
	if err = db.QueryRowContext(ctx, "SELECT id,body FROM trial_learning").Scan(&id, &body); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if id != "stable-identity" || body != "Keep new knowledge" {
		t.Fatal("knowledge changed")
	}
	b, _ := os.ReadFile(filepath.Join(root, ".llewm", "continuity", "config.toml"))
	if !strings.Contains(string(b), "untouched") {
		t.Fatal("unknown config discarded")
	}
	if !exists(filepath.Join(root, ".llewm", "workspace.toml")) {
		t.Fatal("workspace not moved to common root")
	}
	if err = Restore(root, backup, true); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, ".continuity", "workspace.toml")) || exists(filepath.Join(root, ".llewm", "continuity")) {
		t.Fatal("original layout not restored")
	}
	b, _ = os.ReadFile(filepath.Join(root, ".continuity", "config.toml"))
	if strings.Contains(string(b), ".llewm") {
		t.Fatal("original config changed")
	}
	db, err = database.OpenReadOnly(ctx, filepath.Join(root, ".continuity", "continuity.db"))
	if err != nil {
		t.Fatal(err)
	}
	floor, err := database.WriterFloor(ctx, db)
	db.Close()
	if err != nil || floor != 121 {
		t.Fatalf("original writer floor changed: %d %v", floor, err)
	}
}
func TestRollbackRefusesNewKnowledgeAndUnrelatedNewFiles(t *testing.T) {
	for _, kind := range []string{"database", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx, root, backup := legacyFixture(t)
			r, err := Migrate(ctx, root, backup, true)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "database" {
				db, e := database.Open(ctx, filepath.Join(r.Plan.Destination, r.Plan.DatabaseRelative))
				if e != nil {
					t.Fatal(e)
				}
				_, e = db.Exec("INSERT INTO trial_learning VALUES ('later','Do not discard')")
				db.Close()
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if err = os.WriteFile(filepath.Join(r.Plan.Destination, "new-shm"), []byte("Keep this too"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = Restore(root, backup, true); err == nil || !strings.Contains(err.Error(), "new state changed") {
				t.Fatalf("unsafe rollback: %v", err)
			}
			if !exists(r.Plan.Destination) || !exists(filepath.Join(backup, "source")) {
				t.Fatal("state lost on refusal")
			}
		})
	}
}
func TestMigrationFailureRestoresSourceAndCrashReceiptRecovers(t *testing.T) {
	ctx, root, backup := legacyFixture(t)
	calls := 0
	_, err := migrate(ctx, root, backup, true, func(a, b string) error {
		calls++
		if calls == 2 {
			return errors.New("injected second rename failure")
		}
		return os.Rename(a, b)
	})
	if err == nil || !exists(filepath.Join(root, ".continuity", "continuity.db")) {
		t.Fatalf("source not restored after failure: %v", err)
	}
	backup = filepath.Join(t.TempDir(), "backup2")
	r, err := Migrate(ctx, root, backup, true)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the durable receipt/state of a crash after source retirement and
	// before destination activation. Recovery uses the receipt, never guessing.
	if err = os.Rename(r.Plan.Destination, r.Stage); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project.toml", "workspace.toml"} {
		if err = os.Remove(filepath.Join(root, ".llewm", f)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, ".llewm", "layout.lock"), []byte(backup), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = config.Load(root); err == nil {
		t.Fatal("partial migration opened")
	}
	if err = Restore(root, backup, true); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, ".continuity", "continuity.db")) || exists(r.Stage) {
		t.Fatal("crash recovery incomplete")
	}
}
func TestLayoutRejectsCollisionsOverridesAndUnsafePaths(t *testing.T) {
	ctx, root, backup := legacyFixture(t)
	if _, err := Migrate(ctx, root, filepath.Join(root, ".llewm", "backup"), true); err == nil {
		t.Fatal("backup inside new state")
	}
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".continuity"))
	if _, err := Preview(ctx, root); err == nil {
		t.Fatal("override accepted")
	}
	t.Setenv("CONTINUITY_HOME", "")
	if err := os.Mkdir(filepath.Join(root, ".llewm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".llewm", "project.toml"), []byte("foreign layout"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, root, backup, true); err == nil {
		t.Fatal("collision accepted")
	}
	if err := os.Remove(filepath.Join(root, ".llewm", "project.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".continuity", "linked")); err != nil {
		t.Skip(err)
	}
	if _, err := Preview(ctx, root); err == nil {
		t.Fatal("symlink accepted")
	}
}
