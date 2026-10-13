-- A session-start sync that leaves its push, or a fetch that did not fit its
-- budget, to a background sync is recorded as deferred. SQLite cannot alter a
-- CHECK constraint, so the append-only table is rebuilt with its rows intact.

CREATE TABLE sync_attempts_next (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    remote TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('synced', 'offline', 'collision', 'error', 'deferred')),
    exported_records INTEGER NOT NULL CHECK (exported_records >= 0),
    imported_records INTEGER NOT NULL CHECK (imported_records >= 0),
    collisions INTEGER NOT NULL CHECK (collisions >= 0),
    unpushed_commits INTEGER NOT NULL CHECK (unpushed_commits >= 0),
    imported_head TEXT,
    detail TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    deferred_records INTEGER NOT NULL DEFAULT 0 CHECK (deferred_records >= 0)
) STRICT;

INSERT INTO sync_attempts_next(id, project_id, remote, started_at, finished_at, outcome, exported_records, imported_records, collisions, unpushed_commits, imported_head, detail, schema_version, deferred_records)
SELECT id, project_id, remote, started_at, finished_at, outcome, exported_records, imported_records, collisions, unpushed_commits, imported_head, detail, schema_version, deferred_records
FROM sync_attempts;

DROP TABLE sync_attempts;
ALTER TABLE sync_attempts_next RENAME TO sync_attempts;

CREATE INDEX sync_attempts_project_time ON sync_attempts(project_id, started_at DESC, id DESC);

CREATE TRIGGER sync_attempts_no_update BEFORE UPDATE ON sync_attempts BEGIN SELECT RAISE(ABORT,'sync attempts are append-only'); END;
CREATE TRIGGER sync_attempts_no_delete BEFORE DELETE ON sync_attempts BEGIN SELECT RAISE(ABORT,'sync attempts are append-only'); END;
