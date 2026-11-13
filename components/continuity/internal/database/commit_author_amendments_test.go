package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration098CarriesRevocationsAndRepairRequests(t *testing.T) {
	ctx := context.Background()
	db, err := openReadWrite(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	names := migrationNames()
	var before []string
	for _, name := range names {
		if name == "098_commit_author_amendments.sql" {
			break
		}
		before = append(before, name)
	}
	if err := migrate(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,root,name,created_at) VALUES ('p','x','test','2026-01-01T00:00:00Z');
		INSERT INTO commit_authors(project_id,member,commit_sha,session_id,client,task_id,tool_name,certainty,observed_at) VALUES
			('p','','aaa','s','codex','t','Bash','observed','2026-11-01T00:00:00Z'),
			('p','','bbb','s','codex','t','Bash','observed','2026-11-01T00:00:01Z');
		INSERT INTO commit_author_revocations(project_id,member,commit_sha,session_id,reason,revoked_at) VALUES ('p','','aaa','s','pulled','2026-11-02T00:00:00Z');
		DELETE FROM commit_author_revalidations_pending; INSERT INTO commit_author_revalidations_pending(project_id,cutoff) VALUES ('p','2026-11-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, names); err != nil {
		t.Fatal(err)
	}
	var revokes, observations int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM commit_author_amendments WHERE kind='revoke' AND commit_sha='aaa' AND session_id='s' AND task_id='' AND reason='pulled' AND amended_at='2026-11-02T00:00:00Z'), (SELECT count(*) FROM commit_authors)`).Scan(&revokes, &observations); err != nil {
		t.Fatal(err)
	}
	if revokes != 1 || observations != 2 {
		t.Fatalf("revokes=%d observations=%d; the revocation must carry over and observations stay", revokes, observations)
	}
	var leftovers int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('commit_author_revocations','commit_author_revalidations_pending')`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("replaced tables remain: %d %v", leftovers, err)
	}
	repairs := map[string]string{}
	rows, err := db.Query(`SELECT repair,cutoff FROM commit_author_repairs_pending WHERE project_id='p'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var repair, cutoff string
		if err := rows.Scan(&repair, &cutoff); err != nil {
			t.Fatal(err)
		}
		repairs[repair] = cutoff
	}
	rows.Close()
	if repairs["revalidate"] != "2026-11-13T00:00:00Z" || repairs["retask"] == "" || len(repairs) != 2 {
		t.Fatalf("pending repairs = %v", repairs)
	}
	if _, err := db.Exec(`INSERT INTO commit_author_amendments(project_id,member,commit_sha,session_id,kind,task_id,reason,amended_at) VALUES ('p','','aaa','s','revoke','x','bad','2026-11-14T00:00:00Z')`); err == nil {
		t.Fatal("a revoke accepted a task")
	}
	if _, err := db.Exec(`INSERT INTO commit_author_amendments(project_id,member,commit_sha,session_id,kind,reason,amended_at) VALUES ('p','','aaa','s','revoke','again','2026-11-14T00:00:00Z')`); err == nil {
		t.Fatal("a second revoke of the same attribution was accepted")
	}
}
