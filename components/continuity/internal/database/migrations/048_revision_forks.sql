-- A revision fork is one record revision (type, ID, revision) that two machines
-- wrote with different content before syncing. The project keeps one version
-- (is_local = 1: its own, or the proposed one when it had none); every version,
-- that one included, is kept whole here, device-local, so the heads can be shown
-- and any of them accepted later.
CREATE TABLE fork_heads (
    project_id TEXT NOT NULL REFERENCES projects(id),
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL CHECK (record_revision >= 1),
    envelope_sha256 TEXT NOT NULL,
    is_local INTEGER NOT NULL CHECK (is_local IN (0, 1)),
    envelope_json TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, envelope_sha256)
) STRICT;

CREATE INDEX fork_heads_record ON fork_heads(project_id, record_type, record_id, record_revision);

CREATE TRIGGER fork_heads_no_update BEFORE UPDATE ON fork_heads BEGIN SELECT RAISE(ABORT,'fork heads are append-only'); END;
CREATE TRIGGER fork_heads_no_delete BEFORE DELETE ON fork_heads BEGIN SELECT RAISE(ABORT,'fork heads are append-only'); END;

-- Resolutions now also settle revision forks. SQLite cannot alter a CHECK
-- constraint, so the append-only table is rebuilt with its rows intact.
CREATE TABLE fork_resolutions_next (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    family TEXT NOT NULL CHECK (family IN ('checkpoint', 'revision')),
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

INSERT INTO fork_resolutions_next(project_id, id, family, subject_id, accepted_head, rejected_heads_json, continuation_id, resolver, authority, recorded_at, schema_version)
SELECT project_id, id, family, subject_id, accepted_head, rejected_heads_json, continuation_id, resolver, authority, recorded_at, schema_version
FROM fork_resolutions;

DROP TABLE fork_resolutions;
ALTER TABLE fork_resolutions_next RENAME TO fork_resolutions;

CREATE INDEX fork_resolutions_subject ON fork_resolutions(project_id, family, subject_id, recorded_at);

CREATE TRIGGER fork_resolutions_no_update BEFORE UPDATE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;
CREATE TRIGGER fork_resolutions_no_delete BEFORE DELETE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;
