package historystorage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/config"
	"github.com/llewm/llewm/components/continuity/internal/database"
	"github.com/llewm/llewm/components/continuity/internal/gitenv"
	"github.com/llewm/llewm/components/continuity/internal/portable"
	"github.com/llewm/llewm/components/continuity/internal/project"
	"github.com/llewm/llewm/components/continuity/internal/recordsync"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitenv.Command(context.Background(), append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func legacyFixture(t *testing.T) (context.Context, string, Plan) {
	t.Helper()
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "code")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".llewm", "continuity"))
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(parent, "global"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	fixtureGit(t, root, "init", "--quiet", "--template=")
	fixtureGit(t, root, "commit", "--quiet", "--allow-empty", "-m", "code")
	if _, err := config.WriteDefault(root); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(cfg.Storage.Database), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.Storage.Database)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("../database/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name > "090_zz" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("../database/migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err = db.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := (project.Resolver{DB: db}).Ensure(ctx, root, "Fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	const lineage = "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = (portable.Service{DB: db}).Initialize(ctx, binding.ProjectID, lineage); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	object, err := portable.EncodeEnvelope(portable.EnvelopeSpec{LineageID: lineage, RecordType: "decision", RecordSchemaVersion: 1, RecordID: "original", RecordedAt: time.Now().UTC(), Authority: "caller_asserted", Index: []byte(`{"title":"Original","status":"accepted"}`), Payload: []byte(`{"title":"Original","rationale":"Preserve it","disposition":"accepted","actor":"owner","actor_type":"human","review_source":"","links":[],"operational_schema_version":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(config.StateDir(root), "history")
	if err = os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	archive, err := recordsync.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"lineage": []byte(lineage + "\n"), filepath.Join("origins", object.Envelope.EnvelopeSHA256, recordsync.Hash("local")): []byte("observed\n"), portable.MemoryRecordPath(object.Envelope): portable.MemoryFileBytes(object)} {
		if _, err := recordsync.WriteFile(archive, name, body, false); err != nil {
			t.Fatal(err)
		}
	}
	archive.Close()
	remote := filepath.Join(parent, "remote.git")
	fixtureGit(t, parent, "init", "--quiet", "--bare", "--template=", remote)
	clone := filepath.Join(parent, "memory")
	fixtureGit(t, parent, "clone", "--quiet", "--no-hardlinks", "--template=", remote, clone)
	cloneRoot, err := recordsync.OpenRoot(clone)
	if err != nil {
		t.Fatal(err)
	}
	block, err := recordsync.EncodeBlock(lineage, []portable.EncodedEnvelope{object})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recordsync.PublishBlock(cloneRoot, lineage, block); err != nil {
		t.Fatal(err)
	}
	cloneRoot.Close()
	fixtureGit(t, clone, "add", ".")
	fixtureGit(t, clone, "commit", "--quiet", "-m", "history")
	fixtureGit(t, clone, "push", "--quiet", "origin", "HEAD")
	fixtureGit(t, clone, "commit", "--quiet", "--allow-empty", "-m", "unpublished")
	if _, err = config.SetSync(root, config.Sync{Remote: remote, Clone: clone, Manual: true}); err != nil {
		t.Fatal(err)
	}
	p, err := NewPlan(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, root, p
}

func TestLegacyStorageCutoverPreservesSourcesAndResumes(t *testing.T) {
	ctx, root, p := legacyFixture(t)
	if p.ArchiveMode != "legacy" || p.ArchiveInspection.Envelopes != 1 || p.DatabaseProjects != 1 || p.Git.Inspection.State != "working" {
		t.Fatalf("plan: %+v", p)
	}
	clone := p.Git.Inspection.Paths.Clone
	remote := fixtureGit(t, clone, "remote", "get-url", "origin")
	remoteHead := fixtureGit(t, remote, "rev-parse", "HEAD")
	localHead := fixtureGit(t, clone, "rev-parse", "HEAD")
	name := filepath.Join(t.TempDir(), "plan.json")
	if err := SavePlan(name, p); err != nil {
		t.Fatal(err)
	}
	if err := SavePlan(name, p); err == nil {
		t.Fatal("overwrote plan")
	}
	loaded, err := LoadPlan(name)
	if err != nil || loaded.Seal().SHA256 != p.Seal().SHA256 {
		t.Fatalf("load: %v", err)
	}
	if _, err = Prepare(ctx, root, p, false); err == nil {
		t.Fatal("prepared with running writers")
	}
	if _, err = Complete(ctx, root, p, false); err == nil {
		t.Fatal("completed with running writers")
	}
	status, err := database.Inspect(ctx, p.Database.Path)
	if err != nil || status.AppliedMigration != p.Database.AppliedMigration {
		t.Fatalf("planning changed schema: %+v %v", status, err)
	}
	for i := 0; i < 2; i++ {
		result, err := Prepare(ctx, root, p, true)
		if err != nil || result.State != "prepared" || result.Archive.Envelopes != 1 {
			t.Fatalf("prepare %d: %+v %v", i, result, err)
		}
	}
	for _, path := range []string{p.Archive.Source, p.SchemaBackup, p.SchemaBackup + ".verified", p.Archive.DatabaseBackup, filepath.Join(clone, ".git")} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("preparation %s: %v", path, err)
		}
	}
	// Interrupt after the archive switches, before Git switches.
	db, err := database.Open(ctx, p.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (recordsync.Archive{DB: db, ProjectID: p.ProjectID, Lineage: p.LineageID}).CompleteStorageMigration(ctx, p.Archive, true)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := Complete(ctx, root, p, true)
		if err != nil || result.State != "ready" || result.Archive.Envelopes != 1 || result.Git.UniqueEnvelopes != 1 || result.Git.HeadOID != localHead {
			t.Fatalf("complete %d: %+v %v", i, result, err)
		}
	}
	if fixtureGit(t, remote, "rev-parse", "HEAD") != remoteHead {
		t.Fatal("cutover published remote")
	}
	if _, err = os.Stat(filepath.Join(p.Archive.Retained, "lineage")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(p.Git.Inspection.Paths.Retained, ".git")); err != nil {
		t.Fatal(err)
	}
	audit, err := Audit(ctx, root)
	if err != nil || audit.State != "verified" || !audit.Git.FullObjectCheck || !audit.Git.ConversionRequired || len(audit.Git.TransportFormats) != 1 || audit.Git.TransportFormats[0] != 4 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
}

func TestChangedGitSourceBlocksArchiveRetirement(t *testing.T) {
	ctx, root, p := legacyFixture(t)
	if _, err := Prepare(ctx, root, p, true); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, p.Git.Inspection.Paths.Clone, "commit", "--quiet", "--allow-empty", "-m", "late writer")
	if _, err := Complete(ctx, root, p, true); err == nil {
		t.Fatal("accepted changed Git source")
	}
	if _, err := os.Stat(filepath.Join(p.Archive.Source, "lineage")); err != nil {
		t.Fatalf("retired archive before checking Git: %v", err)
	}
}

func TestReadyArchiveSchemaUpgradeAuditsSQLiteBeforeMigration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("CONTINUITY_HOME", filepath.Join(root, ".llewm", "continuity"))
	t.Setenv("CONTINUITY_GLOBAL_HOME", filepath.Join(root, "global"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	fixtureGit(t, root, "init", "--quiet", "--template=")
	fixtureGit(t, root, "commit", "--quiet", "--allow-empty", "-m", "code")
	if _, err := config.WriteDefault(root); err != nil {
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
	binding, err := (project.Resolver{DB: db}).Ensure(ctx, root, "SQLite upgrade", true)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	const lineage = "lineage:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := (portable.Service{DB: db}).Initialize(ctx, binding.ProjectID, lineage); err != nil {
		db.Close()
		t.Fatal(err)
	}
	archive, err := recordsync.OpenArchive(ctx, db, binding.ProjectID, lineage, filepath.Join(config.StateDir(root), "history"))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := archive.AuditStorage(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	// Migration 095 changes only the floor and 096 adds one device-local table.
	// Removing both fixture markers, that table, and restoring 137 produces the
	// exact predecessor schema, without rebuilding it.
	if _, err := db.Exec(`DROP TABLE commit_author_revocations; DELETE FROM schema_migrations WHERE version IN ('095_history_radix_writer.sql','096_commit_author_revocations.sql'); UPDATE writer_floor SET mcp_api=137 WHERE singleton=1`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	p, err := NewPlan(ctx, root)
	if err != nil || p.ArchiveMode != "ready" {
		t.Fatalf("ready plan: %+v %v", p, err)
	}
	if result, err := Prepare(ctx, root, p, true); err != nil || result.State != "prepared" || result.Database == nil || result.Database.BackupMigration != "094_history_recovery_selection.sql" {
		t.Fatalf("ready archive schema upgrade: %+v %v", result, err)
	}
	if result, err := Complete(ctx, root, p, true); err != nil || result.State != "ready" {
		t.Fatalf("finish schema-only upgrade: %+v %v", result, err)
	}
}

func TestCutoverRequiresOriginalPlanAndVerifiedBackup(t *testing.T) {
	ctx, root, p := legacyFixture(t)
	changed := p
	changed.Archive.Retained += "-other"
	if _, err := Prepare(ctx, root, changed, true); err == nil {
		t.Fatal("accepted edited recovery paths")
	}
	if _, err := Prepare(ctx, root, p, true); err != nil {
		t.Fatal(err)
	}
	receipt, err := os.ReadFile(p.SchemaBackup + ".verified")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p.SchemaBackup+".verified", []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(ctx, root, p, true); err == nil {
		t.Fatal("accepted invalid backup receipt")
	}
	if _, err = os.Stat(filepath.Join(p.Archive.Source, "lineage")); err != nil {
		t.Fatal("invalid receipt retired source")
	}
	if err = os.WriteFile(p.SchemaBackup+".verified", receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(ctx, root, p, true); err != nil {
		t.Fatal(err)
	}
}
