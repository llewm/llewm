package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llewm/llewm/components/continuity/internal/decision/decisionhash"
)

// latestMigration is the newest migration this build embeds.
const latestMigration = "083_sync_outcomes.sql"

func migrationsBefore(to string) []string {
	names := []string{}
	for _, name := range migrationNames() {
		if name < to {
			names = append(names, name)
		}
	}
	return names
}

func migrationsAfter(from string) []string {
	names := []string{}
	for _, name := range migrationNames() {
		if name > from {
			names = append(names, name)
		}
	}
	return names
}

func TestCompiledLatestMigration(t *testing.T) {
	latest, err := CompiledLatestMigration()
	if err != nil {
		t.Fatal(err)
	}
	if latest != latestMigration {
		t.Fatalf("compiled latest migration = %q", latest)
	}
}

func TestMigration069RebuildsRawUsageAndPreservesSummaries(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "schema-068.db"))+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(ctx, db, migrationsBefore("069_usage_accuracy.sql")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-05T00:00:00Z')`,
		`INSERT INTO usage_requests(project_id,client,session_id,request_id,model,occurred_at,input_tokens,cache_write_tokens,cache_write_long_tokens,cache_read_tokens,output_tokens,reasoning_tokens,sidechain) VALUES('p','codex','s','r','gpt','2026-10-05T00:00:00Z',1,0,0,0,1,0,0)`,
		`INSERT INTO usage_sources(project_id,client,path,session_id,matched,size,offset,scanned_at) VALUES('p','codex','/rollout','s',1,1,1,'2026-10-05T00:00:00Z')`,
		`INSERT INTO usage_session_summaries(project_id,id,client,session_id,instance_id,first_at,last_at,task_id,overhead_tokens,models_json,tools_json,schema_version) VALUES('p','summary','codex','remote','i','2026-10-05T00:00:00Z','2026-10-05T00:01:00Z','',1,'[]','[]',1)`,
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"usage_requests", "usage_sources"} {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s was not cleared: count=%d err=%v", table, count, err)
		}
	}
	var version int
	if err = db.QueryRowContext(ctx, `SELECT schema_version FROM usage_session_summaries WHERE id='summary'`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema-1 summary was not preserved: version=%d err=%v", version, err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO usage_session_summaries(project_id,id,client,session_id,instance_id,first_at,last_at,task_id,overhead_tokens,models_json,tools_json,schema_version) VALUES('p','corrected','codex','remote','i','2026-10-05T00:00:00Z','2026-10-05T00:01:00Z','',1,'[]','[]',2)`); err != nil {
		t.Fatalf("schema-2 summary was rejected: %v", err)
	}
}

func TestMigration070RestoresWriterCompatibilityFloor(t *testing.T) {
	ctx := context.Background()
	db := createDatabaseThrough(t, filepath.Join(t.TempDir(), "schema-069.db"), "069_usage_accuracy.sql")
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO writer_floor(singleton,mcp_api,raised_at) VALUES(1,105,'2026-10-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrationsBefore("071_agent_presence.sql")); err != nil {
		t.Fatal(err)
	}
	var floor int
	if err := db.QueryRowContext(ctx, `SELECT mcp_api FROM writer_floor WHERE singleton=1`).Scan(&floor); err != nil || floor != 102 {
		t.Fatalf("writer floor after compatibility correction = %d: %v", floor, err)
	}
}

func TestMigration074NormalizesRevisionForkIdentity(t *testing.T) {
	ctx := context.Background()
	db := createDatabaseThrough(t, filepath.Join(t.TempDir(), "schema-073.db"), "073_reviewed_releases.sql")
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-10-07T00:00:00Z')`,
		`INSERT INTO fork_heads(project_id,record_type,record_id,record_revision,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version) VALUES('p','topic_revision','x',2,'env-a1',1,'{"content_sha256":"content-a"}','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','machine_observed',1)`,
		`INSERT INTO fork_heads(project_id,record_type,record_id,record_revision,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version) VALUES('p','topic_revision','x',2,'env-a2',0,'{"content_sha256":"content-a"}','2026-10-02T00:00:00Z','2026-10-02T00:00:00Z','machine_observed',1)`,
		`INSERT INTO fork_heads(project_id,record_type,record_id,record_revision,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version) VALUES('p','topic_revision','x',2,'env-b',0,'{"content_sha256":"content-b"}','2026-10-02T00:00:00Z','2026-10-02T00:00:00Z','machine_observed',1)`,
		`INSERT INTO fork_resolutions(project_id,id,family,subject_id,accepted_head,rejected_heads_json,continuation_id,resolver,authority,recorded_at,schema_version) VALUES('p','resolution','revision','topic_revision:x@2','env-a2','["env-a1","env-b"]',NULL,'owner','user_reported','2026-10-03T00:00:00Z',1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(ctx, db, []string{"074_stable_revision_fork_heads.sql"}); err != nil {
		t.Fatal(err)
	}
	var semantic, observed int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM fork_heads),(SELECT COUNT(*) FROM fork_head_observations)`).Scan(&semantic, &observed); err != nil || semantic != 2 || observed != 3 {
		t.Fatalf("head counts semantic=%d observed=%d: %v", semantic, observed, err)
	}
	var accepted, rejected string
	var schema int
	if err := db.QueryRowContext(ctx, `SELECT accepted_head,rejected_heads_json,schema_version FROM fork_resolutions WHERE id='resolution-stable'`).Scan(&accepted, &rejected, &schema); err != nil || accepted != "content-a" || rejected != `["content-b"]` || schema != 2 {
		t.Fatalf("stable resolution = accepted %q rejected %q schema %d: %v", accepted, rejected, schema, err)
	}
	var floor int
	if err := db.QueryRowContext(ctx, `SELECT mcp_api FROM writer_floor WHERE singleton=1`).Scan(&floor); err != nil || floor != 114 {
		t.Fatalf("writer floor = %d: %v", floor, err)
	}
}

func TestInitializeAppliesMigrations(t *testing.T) {
	db, err := Initialize(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected at least one migration")
	}
}

func TestUpgradeFrom032RetiresContractAuthority(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := createDatabaseThrough(t, path, "032_contract_authority.sql")
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('project-1','/fixture','fixture','2026-09-26T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO contract_authority_migrations(id,project_id,contract_id,sequence,predecessor_id,target_mode,destination,request_key,request_sha256,recorded_at,payload_json) VALUES('migration-1','project-1','client-hooks',1,'genesis','llmem_registry','docs/registry.md','adopt-1','hash','2026-09-26T00:00:00Z','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "before-033.db")
	result, err := Migrate(ctx, path, MigrateOptions{From: "032_contract_authority.sql", To: latestMigration, BackupPath: backup})
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupIntegrity != "ok" || result.DatabaseIntegrity != "ok" || result.ForeignKeyViolations != 0 || !equalStrings(result.AppliedMigrations, migrationsAfter("032_contract_authority.sql")) {
		t.Fatalf("migration result: %#v", result)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	// Contracts became marked Markdown sections; the backup keeps the rows.
	if _, err = upgraded.ExecContext(ctx, `SELECT 1 FROM contract_authority_migrations`); err == nil {
		t.Fatal("contract authority table survived")
	}
	var count int
	if err = upgraded.QueryRowContext(ctx, `SELECT COUNT(*) FROM contract_index_entries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new table: %d %v", count, err)
	}
}

func TestMigrations036And037BackfillClaimsAndHistoryIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := createDatabaseThrough(t, path, "035_backlink_indexes.sql")
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('project-1','/fixture','fixture','2026-09-26T00:00:00Z')`,
		`INSERT INTO work_narratives(id,project_id,title,trigger_kind,occurred_from,creator,semantic_sha256,recorded_at,schema_version,payload_json) VALUES('n1','project-1','Plan cards','commit','2026-09-01T00:00:00Z','test','hash','2026-09-14T00:00:00Z',1,'{"claims":[{"kind":"intent","text":"Compact reminders","authority":"caller_asserted"},{"kind":"decision","text":"Snapshot source hashes","authority":"caller_asserted"}]}')`,
		`INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,recorded_at,schema_version) VALUES('d1','project-1','hash','Keep cards','Cards remain reminders','accepted','owner','human','caller_asserted','','2026-09-15T00:00:00Z',1)`,
		`INSERT INTO checkpoints(id,project_id,task_title,status,summary,next_action,payload_json,created_at) VALUES('c1','project-1','Cards task','completed','Done','None','{}','2026-09-16T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := Migrate(ctx, path, MigrateOptions{From: "035_backlink_indexes.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before-036.db")}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var claims, chunks int
	var ref string
	if err = upgraded.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_narrative_claims`).Scan(&claims); err != nil || claims != 2 {
		t.Fatalf("claims = %d %v", claims, err)
	}
	if err = upgraded.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks`).Scan(&chunks); err != nil || chunks != 4 {
		t.Fatalf("chunks = %d %v", chunks, err)
	}
	var eventTime string
	if err = upgraded.QueryRowContext(ctx, `SELECT ref, recorded_at FROM history_chunks WHERE history_chunks MATCH 'snapshot'`).Scan(&ref, &eventTime); err != nil || ref != "narrative:n1#1" || eventTime != "2026-09-01T00:00:00Z" {
		t.Fatalf("stemmed match = %q at %q %v", ref, eventTime, err)
	}
	// Insert triggers keep later writes indexed.
	if _, err = upgraded.ExecContext(ctx, `INSERT INTO work_items(id,project_id,kind,title,body,creator,authority,recorded_at,schema_version) VALUES('i1','project-1','idea','Ideas view','Show ideas','test','caller_asserted','2026-09-17T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if err = upgraded.QueryRowContext(ctx, `SELECT ref FROM history_chunks WHERE history_chunks MATCH 'ideas'`).Scan(&ref); err != nil || ref != "work_item:i1" {
		t.Fatalf("trigger index = %q %v", ref, err)
	}
}

func TestMigration038BackfillsOnePrimaryRepositoryPerProject(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := createDatabaseThrough(t, path, "037_history_event_time.sql")
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('project-a','/work/a','a','2026-09-01T00:00:00Z')`,
		`INSERT INTO projects(id,root,name,created_at) VALUES('project-b','/work/b','b','2026-09-02T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := Migrate(ctx, path, MigrateOptions{From: "037_history_event_time.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before-038.db")}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	rows, err := upgraded.QueryContext(ctx, `SELECT p.id, r.id, l.path, l.root_commit IS NULL, l.source, l.observed_at
        FROM projects p JOIN repositories r ON r.project_id=p.id AND r.role='primary'
        JOIN repository_locations l ON l.repository_id=r.id ORDER BY p.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var projectID, repositoryID, locationPath, source, observed string
		var unknownCommit bool
		if err := rows.Scan(&projectID, &repositoryID, &locationPath, &unknownCommit, &source, &observed); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(repositoryID, "repo-") || len(repositoryID) != len("repo-")+16 || !unknownCommit || source != "backfill" {
			t.Fatalf("unexpected backfill for %s: repo=%q unknown=%t source=%q", projectID, repositoryID, unknownCommit, source)
		}
		got = append(got, projectID+"@"+locationPath+"@"+observed)
	}
	if strings.Join(got, ",") != "project-a@/work/a@2026-09-01T00:00:00Z,project-b@/work/b@2026-09-02T00:00:00Z" {
		t.Fatalf("backfilled locations = %v", got)
	}
	if _, err := upgraded.ExecContext(ctx, `UPDATE repository_locations SET path='/elsewhere'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("repository locations accepted an update: %v", err)
	}
	if _, err := upgraded.ExecContext(ctx, `DELETE FROM repositories`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("repositories accepted a delete: %v", err)
	}
}

func TestMigration039PreservesRepositoriesAndAddsMembers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db := createDatabaseThrough(t, path, "038_repository_identity.sql")
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('project-a','/work/a','a','2026-09-01T00:00:00Z')`,
		`INSERT INTO repositories(id,project_id,role,created_at,schema_version) VALUES('repo-a','project-a','primary','2026-09-01T00:00:00Z',1)`,
		`INSERT INTO repository_locations(project_id,repository_id,path,root_commit,source,observed_at,schema_version) VALUES('project-a','repo-a','/work/a','abc','observed','2026-09-02T00:00:00Z',1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := Migrate(ctx, path, MigrateOptions{From: "038_repository_identity.sql", To: latestMigration, BackupPath: filepath.Join(t.TempDir(), "before-039.db")}); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var role, location, commit string
	var memberPath sql.NullString
	if err := upgraded.QueryRowContext(ctx, `SELECT r.role, r.member_path, l.path, l.root_commit FROM repositories r JOIN repository_locations l ON l.repository_id=r.id WHERE r.id='repo-a'`).Scan(&role, &memberPath, &location, &commit); err != nil || role != "primary" || memberPath.Valid || location != "/work/a" || commit != "abc" {
		t.Fatalf("preserved repository: %s %v %s %s %v", role, memberPath, location, commit, err)
	}
	if _, err := upgraded.ExecContext(ctx, `INSERT INTO repositories(id,project_id,role,member_path,created_at,schema_version) VALUES('repo-m','project-a','member','api','2026-09-03T00:00:00Z',1)`); err != nil {
		t.Fatalf("member insert: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO repositories(id,project_id,role,member_path,created_at,schema_version) VALUES('repo-x','project-a','member',NULL,'2026-09-03T00:00:00Z',1)`,
		`INSERT INTO repositories(id,project_id,role,member_path,created_at,schema_version) VALUES('repo-y','project-a','primary','api','2026-09-03T00:00:00Z',1)`,
		`INSERT INTO repositories(id,project_id,role,member_path,created_at,schema_version) VALUES('repo-z','project-a','member','api','2026-09-03T00:00:00Z',1)`,
		`INSERT INTO repository_locations(project_id,repository_id,path,source,observed_at,schema_version) VALUES('project-a','repo-missing','/x','observed','2026-09-03T00:00:00Z',1)`,
	} {
		if _, err := upgraded.ExecContext(ctx, statement); err == nil {
			t.Fatalf("accepted invalid row: %s", statement)
		}
	}
	if _, err := upgraded.ExecContext(ctx, `UPDATE repositories SET role='member'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("repositories accepted an update: %v", err)
	}
	if _, err := upgraded.ExecContext(ctx, `DELETE FROM repository_locations`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("repository locations accepted a delete: %v", err)
	}
}

func TestOpenRefusesPendingMigrationWithoutWriting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pending.db")
	db := createDatabaseThrough(t, path, "026_procedure_monitoring.sql")
	db.Close()

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(ctx, path)
	if opened != nil {
		opened.Close()
		t.Fatal("pending database was opened")
	}
	var required *MigrationRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("expected MigrationRequiredError, got %v", err)
	}
	if required.Status.AppliedMigration != "026_procedure_monitoring.sql" || required.Status.CompiledMigration != latestMigration {
		t.Fatalf("unexpected migration requirement: %#v", required.Status)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("strict open changed the pending database")
	}
}

func TestMigrateRequiresExactConfirmationAndVerifiedBackup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "pending.db")
	db := createDatabaseThrough(t, path, "026_procedure_monitoring.sql")
	db.Close()
	backupDir := filepath.Join(root, "backups")
	backup := filepath.Join(backupDir, "continuity-before-027.db")

	if _, err := Migrate(ctx, path, MigrateOptions{From: "025_atomic_completion_terminal.sql", To: latestMigration, BackupPath: backup}); err == nil {
		t.Fatal("migration accepted an incorrect from confirmation")
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmation failure created backup: %v", err)
	}

	result, err := Migrate(ctx, path, MigrateOptions{From: "026_procedure_monitoring.sql", To: latestMigration, BackupPath: backup, BackupRetention: &BackupRetentionOptions{Dir: backupDir, KeepLast: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupMigration != "026_procedure_monitoring.sql" || result.BackupIntegrity != "ok" || result.DatabaseIntegrity != "ok" || result.ForeignKeyViolations != 0 {
		t.Fatalf("unexpected migration verification: %#v", result)
	}
	if !equalStrings(result.AppliedMigrations, migrationsAfter("026_procedure_monitoring.sql")) {
		t.Fatalf("unexpected applied migrations: %#v", result.AppliedMigrations)
	}
	if result.PrunePlan == nil || !result.PrunePlan.DryRun || len(result.PrunePlan.PruneCandidates) != 0 {
		t.Fatalf("migration prune plan: %#v", result.PrunePlan)
	}
	backupStatus, err := Inspect(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	if backupStatus.AppliedMigration != "026_procedure_monitoring.sql" {
		t.Fatalf("backup was not pre-migration: %#v", backupStatus)
	}
	status, err := Inspect(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "current" {
		t.Fatalf("migrated database is not current: %#v", status)
	}
}

func TestBackupCreatesVerifiedNonOverwritingSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "state.db")
	db, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('project-1','/fixture','fixture','2026-10-07T00:00:00Z')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(root, "backups", "snapshot.db")
	result, err := Backup(ctx, path, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != path || result.BackupPath != backupPath || result.BackupMigration != latestMigration || result.BackupIntegrity != "ok" {
		t.Fatalf("backup receipt: %#v", result)
	}
	backupDB, err := openReadOnly(ctx, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var name string
	if err := backupDB.QueryRowContext(ctx, `SELECT name FROM projects WHERE id='project-1'`).Scan(&name); err != nil || name != "fixture" {
		t.Fatalf("snapshot contents = %q: %v", name, err)
	}

	if _, err := Backup(ctx, path, backupPath); err == nil || !strings.Contains(err.Error(), "backup already exists") {
		t.Fatalf("backup overwrite error = %v", err)
	}
	if _, err := Backup(ctx, path, path); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("same-path backup error = %v", err)
	}
}

func TestNextBackupPathAvoidsExistingGeneratedNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 7, 9, 30, 0, 123, time.UTC)
	first, err := NextBackupPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "continuity-20261007T093000.000000123Z.db" {
		t.Fatalf("first generated path = %s", first)
	}
	if err := os.WriteFile(first, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := NextBackupPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second) != "continuity-20261007T093000.000000123Z-2.db" {
		t.Fatalf("second generated path = %s", second)
	}
}

func createDatabaseThrough(t *testing.T, path, target string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL) STRICT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, name := range migrationNames() {
		if name > target {
			break
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			db.Close()
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func TestMigration013PreservesSchema010Records(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-010.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() > "010_checkpoint_action_backfill_scope.sql" {
			continue
		}
		body, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, entry.Name(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('project','/project','project','2026-09-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO checkpoints(id,project_id,task_id,task_title,status,summary,next_action,payload_json,created_at) VALUES('checkpoint','project','task','Existing','completed','kept','none','{}','2026-09-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	var summary, latest string
	if err = db.QueryRowContext(ctx, `SELECT summary FROM checkpoints WHERE id='checkpoint'`).Scan(&summary); err != nil || summary != "kept" {
		t.Fatalf("existing checkpoint was not preserved: %q %v", summary, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latest); err != nil || latest != latestMigration {
		t.Fatalf("unexpected latest migration %q: %v", latest, err)
	}
	for _, table := range []string{"work_items", "work_item_tags", "work_item_dispositions", "work_item_links", "project_memory_identities", "portable_import_receipts", "task_lineage_events"} {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %s after migration: %v", table, err)
		}
	}
}

func TestMigration056ConvertsIdeasSuggestionsAndLinks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-012.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() > "012_ideas.sql" {
			continue
		}
		body, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, entry.Name(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('project','/project','project','2026-09-18T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO ideas(id,project_id,title,body,creator,authority,recorded_at,schema_version) VALUES('idea','project','Existing idea','kept','human','caller_asserted','2026-09-18T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO idea_dispositions(id,project_id,idea_id,status,reason,target_refs_json,creator,authority,recorded_at,schema_version) VALUES('old-disposition','project','idea','integrated','kept','[]','human','caller_asserted','2026-09-18T00:01:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db, migrationsBefore("056_work_items.sql")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO idea_dispositions(id,project_id,idea_id,status,reason,target_refs_json,creator,authority,recorded_at,schema_version) VALUES('accepted','project','idea','accepted','planned','["roadmap:accepted"]','human','caller_asserted','2026-09-18T00:00:30Z',1)`,
		`INSERT INTO idea_tags(project_id,idea_id,tag) VALUES('project','idea','memory')`,
		`INSERT INTO agent_suggestions(id,project_id,kind,summary,rationale,creator,actor_type,idea_id,authority,semantic_sha256,recorded_at,schema_version) VALUES('suggestion','project','next_action','Suggested','Because','codex','agent','idea','model_suggested','x','2026-09-18T00:02:00Z',1)`,
		`INSERT INTO agent_suggestion_dispositions(id,project_id,suggestion_id,status,reason,target_refs_json,creator,authority,recorded_at,schema_version) VALUES('rejected','project','suggestion','rejected','No','[]','human','caller_asserted','2026-09-18T00:03:00Z',1)`,
		`INSERT INTO topic_links(id,project_id,topic_id,target_kind,target_id,target_revision,status,creator,authority,recorded_at) VALUES('topic-link','project','topic','idea','idea','','accepted','human','caller_asserted','2026-09-18T00:04:00Z')`,
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed pre-056 rows: %v", err)
		}
	}
	decisionFields := decisionhash.Fields{ID: "decision", ProjectID: "project", Title: "Decide", Rationale: "Why", Disposition: "accepted", Actor: "human", ActorType: "human", Authority: "caller_asserted", ReviewSource: "", Links: []decisionhash.Link{{Kind: "resulted_in", TargetKind: "idea", TargetID: "idea", TargetRevision: "", Authority: "caller_asserted"}}}
	oldHash, err := decisionhash.Hash(decisionFields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO decision_records(id,project_id,record_sha256,title,rationale,disposition,actor,actor_type,authority,review_source,recorded_at,schema_version) VALUES('decision','project',?,'Decide','Why','accepted','human','human','caller_asserted','','2026-09-18T00:05:00Z',1)`, oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO decision_links(project_id,decision_id,ordinal,kind,target_kind,target_id,target_revision,authority) VALUES('project','decision',0,'resulted_in','idea','idea','','caller_asserted')`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	var kind, status, tags, suggestionStatus, suggestionTags, link, decisionKind, decisionHash, topicKind string
	if err = db.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id='idea'`).Scan(&kind); err != nil || kind != "idea" {
		t.Fatalf("existing idea was not converted: %q %v", kind, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT group_concat(status,',') FROM (SELECT status FROM work_item_dispositions WHERE item_id='idea' ORDER BY recorded_at)`).Scan(&status); err != nil || status != "accepted,integrated" {
		t.Fatalf("idea dispositions were not converted: %q %v", status, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT group_concat(tag,',') FROM work_item_tags WHERE item_id='idea'`).Scan(&tags); err != nil || tags != "memory" {
		t.Fatalf("idea tags were not converted: %q %v", tags, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT d.status,(SELECT group_concat(tag,',') FROM (SELECT tag FROM work_item_tags WHERE item_id='suggestion' ORDER BY tag)),(SELECT relationship||' '||target_ref FROM work_item_links WHERE item_id='suggestion') FROM work_item_dispositions d WHERE d.item_id='suggestion'`).Scan(&suggestionStatus, &suggestionTags, &link); err != nil || suggestionStatus != "discarded" || suggestionTags != "next-action,suggestion" || link != "follow_up_to work_item:idea" {
		t.Fatalf("suggestion was not converted: %q %q %q %v", suggestionStatus, suggestionTags, link, err)
	}
	decisionFields.Links[0].TargetKind = "work_item"
	newHash, err := decisionhash.Hash(decisionFields)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT l.target_kind,d.record_sha256 FROM decision_links l JOIN decision_records d ON d.id=l.decision_id`).Scan(&decisionKind, &decisionHash); err != nil || decisionKind != "work_item" || decisionHash != newHash || newHash == oldHash {
		t.Fatalf("decision link was not converted and rehashed: %q %q want %q %v", decisionKind, decisionHash, newHash, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE decision_records SET title='changed'`); err == nil {
		t.Fatal("decision append-only trigger was not restored")
	}
	if err = db.QueryRowContext(ctx, `SELECT target_kind FROM topic_links WHERE id='topic-link'`).Scan(&topicKind); err != nil || topicKind != "work_item" {
		t.Fatalf("topic link was not converted: %q %v", topicKind, err)
	}
	var chunks int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE family='work_item'`).Scan(&chunks); err != nil || chunks != 2 {
		t.Fatalf("history chunks were not reindexed: %d %v", chunks, err)
	}
	var latest string
	if err = db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latest); err != nil || latest != latestMigration {
		t.Fatalf("unexpected latest migration %q: %v", latest, err)
	}
}

func TestMigration014PreservesInvocationAndAddsPortableFields(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-013.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() > "013_friction_dispositions.sql" {
			continue
		}
		body, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, entry.Name(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO projects(id,root,name,created_at) VALUES('project','/project','project','2026-09-18T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO actor_invocations(id,project_id,actor_type,identity_source,occurred_at,received_at,schema_version) VALUES('existing','project','agent','observed','2026-09-18T00:00:00Z','2026-09-18T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	var id string
	var effort sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT id,effort_requested_raw FROM actor_invocations WHERE id='existing'`).Scan(&id, &effort); err != nil || id != "existing" || effort.Valid {
		t.Fatalf("existing invocation was not preserved with unknown effort: %q %#v %v", id, effort, err)
	}
	var latest string
	if err = db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&latest); err != nil || latest != latestMigration {
		t.Fatalf("unexpected latest migration %q: %v", latest, err)
	}
}

func TestMigration057RequiresNarratedEvidenceThenCutsEvidenceAndCards(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-056.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrate(ctx, db, migrationsBefore("057_cut_evidence_and_cards.sql")); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id,root,name,created_at) VALUES('p','/p','p','2026-09-18T00:00:00Z')`,
		`INSERT INTO evidence_records(id,project_id,record_type,commit_id,title,creator,content_authority,commit_authority,source_kind,content_sha256,recorded_at,commit_at,schema_version,payload_json) VALUES('evidence','p','commit_narrative','0123456789abcdef0123456789abcdef01234567','title','human','caller_asserted','machine_observed','manual','hash','2026-09-18T00:00:00Z','2026-09-18T00:00:00Z',1,'{}')`,
		`INSERT INTO reference_cards(project_id,card_id,revision,title,body,status,profile,creator,authority,recorded_at,schema_version) VALUES('p','card',1,'Card','Body','accepted','general','owner','caller_asserted','2026-09-18T00:00:00Z',1)`,
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrate(ctx, db, migrationNames()); err == nil || !strings.Contains(err.Error(), "evidence record evidence (commit 0123456789abcdef0123456789abcdef01234567) has no work narrative") {
		t.Fatalf("unnarrated evidence was not refused: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO work_narratives(id,project_id,title,trigger_kind,creator,semantic_sha256,recorded_at,schema_version,payload_json) VALUES('gitnote','p','title','commit','human','hash','2026-09-18T00:00:00Z',1,'{}')`,
		`INSERT INTO work_narrative_anchors(project_id,narrative_id,ordinal,anchor_kind,target,target_revision,authority) VALUES('p','gitnote',0,'commit','0123456789abcdef0123456789abcdef01234567','','machine_observed')`,
	} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrate(ctx, db, migrationNames()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"evidence_records", "evidence_record_paths", "reference_cards", "reference_card_links"} {
		var count int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s survived migration 057: %d %v", table, count, err)
		}
	}
	var chunks int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_chunks WHERE family='card'`).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatalf("card history chunks remain: %d %v", chunks, err)
	}
}

func TestBacklinkReverseIndexesAreInstalled(t *testing.T) {
	db, err := Initialize(context.Background(), filepath.Join(t.TempDir(), "indexed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"work_items_task_origin", "work_items_checkpoint_origin", "work_items_roadmap_origin", "track_plan_revisions_task_origin", "track_plan_work_items_target"} {
		var found string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&found); err != nil || found != name {
			t.Fatalf("missing index %s: %v", name, err)
		}
	}
}
