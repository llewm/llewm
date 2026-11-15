package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/llewm/llewm/components/continuity/internal/app"
	"github.com/llewm/llewm/components/continuity/internal/buildinfo"
	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/updatecheck"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationGeneratesVerifiedBackupAndKeepsPrunePreview(t *testing.T) {
	migrations, err := filepath.Abs("../../internal/database/migrations")
	if err != nil {
		t.Fatal(err)
	}
	root := testProject(t)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(cfg.Storage.Database), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.Storage.Database))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TEXT NOT NULL) STRICT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	files, err := os.ReadDir(migrations)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	previous := ""
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".sql") || file.Name() == latestSchema {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrations, file.Name()))
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			db.Close()
			t.Fatalf("fixture %s: %v", file.Name(), err)
		}
		if _, err = db.Exec(`INSERT INTO schema_migrations VALUES(?,'fixture')`, file.Name()); err != nil {
			db.Close()
			t.Fatal(err)
		}
		previous = file.Name()
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var receipt database.MigrateResult
	if err = json.Unmarshal([]byte(runCLI(t, "", "database", "migrate", "--from", previous, "--to", latestSchema)), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.BackupIntegrity != "ok" || receipt.DatabaseIntegrity != "ok" || filepath.Dir(receipt.BackupPath) != cfg.Backups.Dir || receipt.PrunePlan == nil || !receipt.PrunePlan.DryRun || receipt.BackupRetentionAction != "preview_only_no_backups_deleted" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if !strings.Contains(runCLI(t, "", "database", "status", "--output", "json"), `"state": "current"`) {
		t.Fatal("migration not applied")
	}
}

func TestRegistryFailureDoesNotBlockLocalHookOrWalkKnownHistory(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	global := os.Getenv("CONTINUITY_GLOBAL_HOME")
	registry := filepath.Join(global, project.RegistryFileName)
	if err := os.WriteFile(registry, []byte("malformed registry fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "git-trace")
	t.Setenv("GIT_TRACE", filepath.ToSlash(trace))
	a, err := app.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if a.RegistryWarning == "" {
		t.Fatal("missing optional registry diagnosis")
	}
	a.Close()
	data, _ := os.ReadFile(trace)
	if strings.Contains(string(data), "rev-list") {
		t.Fatalf("known location walked Git history: %s", data)
	}
	body := `{"session_id":"registry-fixture","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo hello"}}`
	runCLI(t, body, "hook", "claude")
	a, err = app.OpenReadOnly(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var count int
	if err = a.DB.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id='registry-fixture'`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("local capture failed: %d %v", count, err)
	}
}

func TestMigrationFailureNamesItsGeneratedVerifiedBackup(t *testing.T) {
	root := testProject(t)
	runCLI(t, "", "init")
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.Storage.Database))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM schema_migrations WHERE version=?`, latestSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	status, err := database.Inspect(context.Background(), cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	c := newRootCommand("llewm")
	c.SetArgs([]string{"database", "migrate", "--from", status.AppliedMigration, "--to", latestSchema})
	err = c.Execute()
	if err == nil || !strings.Contains(err.Error(), "verified backup retained at "+cfg.Backups.Dir) {
		t.Fatalf("missing recovery path: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(cfg.Backups.Dir, "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups=%v %v", backups, err)
	}
}

func TestDoctorJSONOnUninitializedProjectAndVersionAlias(t *testing.T) {
	testProject(t)
	c := newRootCommand("llewm")
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"doctor", "--output", "json"})
	if err := c.Execute(); err == nil || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "uninitialized") {
		t.Fatalf("doctor=%s", out.String())
	}
	if runCLI(t, "", "--version") != runCLI(t, "", "version") {
		t.Fatal("version alias differs")
	}
	c = newRootCommand("llewm")
	c.SetArgs([]string{"sync", "init"})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "llewm init") {
		t.Fatalf("missing initialization hint: %v", err)
	}
}

func TestJoinHintNeverPrintsCredentials(t *testing.T) {
	ssh := joinHint("lineage:fixture", "ssh://git:private-value@example.test/memory.git")
	if !strings.Contains(ssh, "ssh://git@example.test/memory.git") || strings.Contains(ssh, "private-value") {
		t.Fatalf("SSH account hint: %s", ssh)
	}
	hint := joinHint("lineage:fixture", "https://person:private-value@example.test/memory.git?token=private-query#private-fragment")
	for _, secret := range []string{"person", "private-value", "private-query", "private-fragment"} {
		if strings.Contains(hint, secret) {
			t.Fatalf("credential in hint: %s", hint)
		}
	}
	if !strings.Contains(hint, "example.test/memory.git") || !strings.Contains(hint, "--lineage") {
		t.Fatal(hint)
	}
}

func TestUpdatePlanInventoriesDistinctStoresWithoutWrites(t *testing.T) {
	ctx := context.Background()
	testProject(t)
	t.Setenv("CONTINUITY_HOME", "")
	var entries []project.RegistryEntry
	before := map[string][]byte{}
	for _, name := range []string{"current", "pending"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		db, err := database.Initialize(ctx, cfg.Storage.Database)
		if err != nil {
			t.Fatal(err)
		}
		if name == "pending" {
			if _, err = db.Exec(`DELETE FROM schema_migrations WHERE version=?`, latestSchema); err != nil {
				t.Fatal(err)
			}
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		before[cfg.Storage.Database], err = os.ReadFile(cfg.Storage.Database)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, project.RegistryEntry{Root: root, Store: cfg.Storage.Database})
	}
	entries = append(entries, entries[0], project.RegistryEntry{Root: filepath.Join(t.TempDir(), "missing"), Store: filepath.Join(t.TempDir(), "missing.db")})
	api := buildinfo.WriterAPIVersion
	rows := storeUpdates(ctx, entries, updatecheck.Report{TargetWriterAPI: &api, TargetMigration: latestSchema})
	if len(rows) != 3 || rows[0].Database.State != "current" || rows[1].Database.State != "pending" || rows[1].MigrationCommand == "" || rows[2].Problem == "" {
		t.Fatalf("inventory=%+v", rows)
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("inventory changed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(entries[3].Store); !os.IsNotExist(err) {
		t.Fatalf("created missing store: %v", err)
	}
}
