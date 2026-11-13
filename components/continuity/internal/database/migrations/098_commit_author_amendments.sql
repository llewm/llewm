-- commit_author_amendments replaces commit_author_revocations with one
-- append-only correction record for commit_authors observations, which stay
-- unchanged. kind 'revoke' withdraws a session's attribution (as 096 did);
-- kind 'retask' keeps the session and names the commit's task instead, '' for
-- none. A retask is inferred from checkpoints, never observed. Device-local,
-- like commit_authors. Readers apply amendments with authorship's
-- attributionsSQL, not a view: History snapshot reuse accepts only reviewed
-- views.
CREATE TABLE commit_author_amendments (
    id INTEGER PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    member TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('revoke', 'retask')),
    task_id TEXT NOT NULL DEFAULT '' CHECK (kind = 'retask' OR task_id = ''),
    reason TEXT NOT NULL,
    amended_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX commit_author_amendments_revoke ON commit_author_amendments(project_id, member, commit_sha, session_id) WHERE kind = 'revoke';
CREATE INDEX commit_author_amendments_commit ON commit_author_amendments(project_id, member, commit_sha, session_id, id);
INSERT INTO commit_author_amendments(project_id,member,commit_sha,session_id,kind,reason,amended_at)
SELECT project_id, member, commit_sha, session_id, 'revoke', reason, revoked_at FROM commit_author_revocations ORDER BY revoked_at, commit_sha;
DROP TABLE commit_author_revocations;

-- commit_author_repairs_pending replaces commit_author_revalidations_pending:
-- one row per project and one-time repair. 'revalidate' is 097's reflog check;
-- 'retask' gives attributions observed before cutoff the session's next
-- checkpointed task when they credit a task already finished when the commit
-- was observed, or no task at all. The next hook runs them.
CREATE TABLE commit_author_repairs_pending (
    project_id TEXT NOT NULL REFERENCES projects(id),
    repair TEXT NOT NULL CHECK (repair IN ('revalidate', 'retask')),
    cutoff TEXT NOT NULL,
    PRIMARY KEY (project_id, repair)
) STRICT;
INSERT INTO commit_author_repairs_pending(project_id,repair,cutoff)
SELECT project_id, 'revalidate', cutoff FROM commit_author_revalidations_pending;
DROP TABLE commit_author_revalidations_pending;
INSERT INTO commit_author_repairs_pending(project_id,repair,cutoff)
SELECT id, 'retask', strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM projects;
