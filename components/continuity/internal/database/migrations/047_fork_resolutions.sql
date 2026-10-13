-- A fork is a record with more than one current head, usually because two
-- machines continued it before syncing. A resolution keeps every head in history,
-- names the accepted one and the rejected ones, and (for checkpoints) the
-- continuation written from the accepted head. Append-only; syncs as
-- fork_resolution.
CREATE TABLE fork_resolutions (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    family TEXT NOT NULL CHECK (family IN ('checkpoint')),
    subject_id TEXT NOT NULL,
    accepted_head TEXT NOT NULL,
    rejected_heads_json TEXT NOT NULL,
    continuation_id TEXT,
    resolver TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('user_reported', 'caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id)
) STRICT;

CREATE INDEX fork_resolutions_subject ON fork_resolutions(project_id, family, subject_id, recorded_at);

CREATE TRIGGER fork_resolutions_no_update BEFORE UPDATE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;
CREATE TRIGGER fork_resolutions_no_delete BEFORE DELETE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;
