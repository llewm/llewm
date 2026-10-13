-- Each history sync attempt is recorded append-only. imported_head is the memory
-- repository commit whose records the project has imported; the next attempt only
-- reads files added after it.
CREATE TABLE sync_attempts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    remote TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('synced', 'offline', 'collision', 'error')),
    exported_records INTEGER NOT NULL CHECK (exported_records >= 0),
    imported_records INTEGER NOT NULL CHECK (imported_records >= 0),
    collisions INTEGER NOT NULL CHECK (collisions >= 0),
    unpushed_commits INTEGER NOT NULL CHECK (unpushed_commits >= 0),
    imported_head TEXT,
    detail TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX sync_attempts_project_time ON sync_attempts(project_id, started_at DESC, id DESC);

CREATE TRIGGER sync_attempts_no_update BEFORE UPDATE ON sync_attempts BEGIN SELECT RAISE(ABORT,'sync attempts are append-only'); END;
CREATE TRIGGER sync_attempts_no_delete BEFORE DELETE ON sync_attempts BEGIN SELECT RAISE(ABORT,'sync attempts are append-only'); END;
