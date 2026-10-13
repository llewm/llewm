-- Revision-fork identity must survive portable envelope rewrites. Preserve every
-- observed envelope for audit and legacy-resolution translation, while keying
-- semantic heads by record identity plus the payload content hash.
CREATE TABLE fork_head_observations (
    project_id TEXT NOT NULL REFERENCES projects(id),
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL CHECK (record_revision >= 1),
    content_sha256 TEXT NOT NULL,
    envelope_sha256 TEXT NOT NULL,
    is_local INTEGER NOT NULL CHECK (is_local IN (0, 1)),
    envelope_json TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, envelope_sha256)
) STRICT;

INSERT INTO fork_head_observations(project_id,record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version)
SELECT project_id,record_type,record_id,record_revision,json_extract(envelope_json,'$.content_sha256'),envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version
FROM fork_heads;

CREATE INDEX fork_head_observations_record ON fork_head_observations(project_id,record_type,record_id,record_revision);
CREATE TRIGGER fork_head_observations_no_update BEFORE UPDATE ON fork_head_observations BEGIN SELECT RAISE(ABORT,'fork head observations are append-only'); END;
CREATE TRIGGER fork_head_observations_no_delete BEFORE DELETE ON fork_head_observations BEGIN SELECT RAISE(ABORT,'fork head observations are append-only'); END;

CREATE TABLE fork_heads_next (
    project_id TEXT NOT NULL REFERENCES projects(id),
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL CHECK (record_revision >= 1),
    content_sha256 TEXT NOT NULL,
    envelope_sha256 TEXT NOT NULL,
    is_local INTEGER NOT NULL CHECK (is_local IN (0, 1)),
    envelope_json TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id,record_type,record_id,record_revision,content_sha256)
) STRICT;

INSERT INTO fork_heads_next(project_id,record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version)
SELECT project_id,record_type,record_id,record_revision,content_sha256,envelope_sha256,is_local,envelope_json,recorded_at,observed_at,authority,schema_version
FROM (
    SELECT o.*, ROW_NUMBER() OVER (
        PARTITION BY project_id,record_type,record_id,record_revision,content_sha256
        ORDER BY is_local DESC, recorded_at DESC, envelope_sha256 DESC
    ) AS keep
    FROM fork_head_observations o
)
WHERE keep=1;

DROP TABLE fork_heads;
ALTER TABLE fork_heads_next RENAME TO fork_heads;
CREATE INDEX fork_heads_record ON fork_heads(project_id,record_type,record_id,record_revision);
CREATE TRIGGER fork_heads_no_update BEFORE UPDATE ON fork_heads BEGIN SELECT RAISE(ABORT,'fork heads are append-only'); END;
CREATE TRIGGER fork_heads_no_delete BEFORE DELETE ON fork_heads BEGIN SELECT RAISE(ABORT,'fork heads are append-only'); END;

-- Refuse migration rather than silently lose a legacy envelope-to-content
-- mapping. Sync records every revision head before importing its resolution.
CREATE TABLE migration_074_guard (ok INTEGER NOT NULL CHECK (ok=1)) STRICT;
INSERT INTO migration_074_guard(ok)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM fork_resolutions f
    LEFT JOIN fork_head_observations accepted
      ON accepted.project_id=f.project_id AND accepted.envelope_sha256=f.accepted_head
    WHERE f.family='revision' AND f.schema_version=1 AND (
        accepted.content_sha256 IS NULL OR EXISTS (
            SELECT 1 FROM json_each(f.rejected_heads_json) rejected
            LEFT JOIN fork_head_observations observed
              ON observed.project_id=f.project_id AND observed.envelope_sha256=CAST(rejected.value AS TEXT)
            WHERE observed.content_sha256 IS NULL
        )
    )
) THEN 0 ELSE 1 END;
DROP TABLE migration_074_guard;

-- Existing portable v1 resolutions remain byte-stable. Append a deterministic
-- schema-2 normalization with content-hash heads so a future format conversion
-- and a newly joined machine do not need the old envelope bytes.
DROP VIEW superseded_records;
DROP VIEW fork_rejected_records;

CREATE TABLE fork_resolutions_next (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    family TEXT NOT NULL CHECK (family IN ('checkpoint', 'revision', 'supersession')),
    subject_id TEXT NOT NULL,
    accepted_head TEXT NOT NULL,
    rejected_heads_json TEXT NOT NULL,
    continuation_id TEXT,
    resolver TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('user_reported', 'caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (
        (family='revision' AND schema_version IN (1,2)) OR
        (family<>'revision' AND schema_version=1)
    ),
    PRIMARY KEY (project_id, id)
) STRICT;

INSERT INTO fork_resolutions_next(project_id,id,family,subject_id,accepted_head,rejected_heads_json,continuation_id,resolver,authority,recorded_at,schema_version)
SELECT project_id,id,family,subject_id,accepted_head,rejected_heads_json,continuation_id,resolver,authority,recorded_at,schema_version
FROM fork_resolutions;

INSERT INTO fork_resolutions_next(project_id,id,family,subject_id,accepted_head,rejected_heads_json,continuation_id,resolver,authority,recorded_at,schema_version)
SELECT project_id,id || '-stable',family,subject_id,accepted_content,rejected_content,continuation_id,resolver,authority,recorded_at,2
FROM (
    SELECT f.*,
        accepted.content_sha256 AS accepted_content,
        (
            SELECT json_group_array(content_sha256)
            FROM (
                SELECT DISTINCT observed.content_sha256
                FROM json_each(f.rejected_heads_json) rejected
                JOIN fork_head_observations observed
                  ON observed.project_id=f.project_id AND observed.envelope_sha256=CAST(rejected.value AS TEXT)
                WHERE observed.content_sha256<>accepted.content_sha256
                ORDER BY observed.content_sha256
            )
        ) AS rejected_content
    FROM fork_resolutions f
    JOIN fork_head_observations accepted
      ON accepted.project_id=f.project_id AND accepted.envelope_sha256=f.accepted_head
    WHERE f.family='revision' AND f.schema_version=1
)
WHERE json_array_length(rejected_content)>0;

DROP TABLE fork_resolutions;
ALTER TABLE fork_resolutions_next RENAME TO fork_resolutions;
CREATE INDEX fork_resolutions_subject ON fork_resolutions(project_id, family, subject_id, recorded_at);
CREATE TRIGGER fork_resolutions_no_update BEFORE UPDATE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;
CREATE TRIGGER fork_resolutions_no_delete BEFORE DELETE ON fork_resolutions BEGIN SELECT RAISE(ABORT,'fork resolutions are append-only'); END;

CREATE VIEW fork_rejected_records AS
SELECT f.project_id, substr(f.subject_id, 1, instr(f.subject_id, ':') - 1) AS family, CAST(j.value AS TEXT) AS id,
    f.accepted_head AS accepted_id, f.id AS resolution_id, f.recorded_at
FROM fork_resolutions f, json_each(f.rejected_heads_json) j
WHERE f.family = 'supersession';

CREATE VIEW superseded_records AS
SELECT project_id, 'decision' AS family, supersedes_id AS id, recorded_at AS at FROM decision_records WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, 'narrative', supersedes_id, recorded_at FROM work_narratives WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, family, id, recorded_at FROM fork_rejected_records;

-- Schema-2 revision resolutions are write-incompatible with API 103 writers.
-- Raise the floor during migration so an already-running pre-114 process stops
-- before it can append an envelope-hash resolution to the migrated store.
INSERT INTO writer_floor(singleton,mcp_api,raised_at)
VALUES(1,114,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT(singleton) DO UPDATE SET mcp_api=excluded.mcp_api,raised_at=excluded.raised_at
WHERE writer_floor.mcp_api<excluded.mcp_api;
