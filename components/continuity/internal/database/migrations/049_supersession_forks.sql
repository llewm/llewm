-- A supersession fork is one record that two machines each superseded before
-- syncing. Both successors must be stored on every machine (later records can
-- reference either), so the one-successor unique constraints go; local writes
-- still refuse to supersede a record that is no longer current.

-- decision_records declares its constraint on the table, so it is rebuilt, with
-- decision_links rebuilt first-referenced so the renames carry both foreign
-- keys. Rows are copied before the index trigger exists, so history chunks are
-- not duplicated.
CREATE TABLE decision_records_v2 (
 id TEXT NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id),
 record_sha256 TEXT NOT NULL,
 title TEXT NOT NULL,
 rationale TEXT NOT NULL,
 disposition TEXT NOT NULL CHECK(disposition IN ('accepted','deferred','rejected','superseded')),
 actor TEXT NOT NULL,
 actor_type TEXT NOT NULL CHECK(actor_type = 'human'),
 authority TEXT NOT NULL CHECK(authority IN ('caller_asserted','user_reported')),
 review_source TEXT NOT NULL,
 supersedes_id TEXT,
 recorded_at TEXT NOT NULL,
 schema_version INTEGER NOT NULL CHECK(schema_version = 1),
 PRIMARY KEY(project_id,id),
 FOREIGN KEY(supersedes_id,project_id) REFERENCES decision_records_v2(id,project_id)
) STRICT;

CREATE TABLE decision_links_v2 (
 project_id TEXT NOT NULL,
 decision_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
 kind TEXT NOT NULL CHECK(kind IN ('subject','supports','opposes','resulted_in','supersedes')),
 target_kind TEXT NOT NULL CHECK(target_kind IN ('task','checkpoint','roadmap_node','track_plan','suggestion','idea','narrative','evidence','commit','decision')),
 target_id TEXT NOT NULL,
 target_revision TEXT NOT NULL,
 authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
 PRIMARY KEY(project_id,decision_id,ordinal),
 FOREIGN KEY(decision_id,project_id) REFERENCES decision_records_v2(id,project_id)
) STRICT;

INSERT INTO decision_records_v2(id, project_id, record_sha256, title, rationale, disposition, actor, actor_type, authority, review_source, supersedes_id, recorded_at, schema_version)
SELECT id, project_id, record_sha256, title, rationale, disposition, actor, actor_type, authority, review_source, supersedes_id, recorded_at, schema_version FROM decision_records;

INSERT INTO decision_links_v2(project_id, decision_id, ordinal, kind, target_kind, target_id, target_revision, authority)
SELECT project_id, decision_id, ordinal, kind, target_kind, target_id, target_revision, authority FROM decision_links;

DROP TABLE decision_links;
DROP TABLE decision_records;
ALTER TABLE decision_records_v2 RENAME TO decision_records;
ALTER TABLE decision_links_v2 RENAME TO decision_links;

CREATE INDEX decision_project_time ON decision_records(project_id,recorded_at DESC,id DESC);
CREATE INDEX decision_records_successor ON decision_records(project_id,supersedes_id) WHERE supersedes_id IS NOT NULL;
CREATE INDEX decision_links_target ON decision_links(project_id,target_kind,target_id,target_revision,decision_id);
CREATE TRIGGER decision_records_no_update BEFORE UPDATE ON decision_records BEGIN SELECT RAISE(ABORT,'decision history is append-only'); END;
CREATE TRIGGER decision_records_no_delete BEFORE DELETE ON decision_records BEGIN SELECT RAISE(ABORT,'decision history is append-only'); END;
CREATE TRIGGER decision_links_no_update BEFORE UPDATE ON decision_links BEGIN SELECT RAISE(ABORT,'decision links are append-only'); END;
CREATE TRIGGER decision_links_no_delete BEFORE DELETE ON decision_links BEGIN SELECT RAISE(ABORT,'decision links are append-only'); END;
CREATE TRIGGER decision_records_index AFTER INSERT ON decision_records
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    VALUES (NEW.title, NEW.rationale, NEW.project_id, 'decision', 'decision:' || NEW.id, NEW.disposition, NEW.recorded_at);
END;

DROP INDEX evidence_records_one_successor;
CREATE INDEX evidence_records_successor ON evidence_records(supersedes_id) WHERE supersedes_id IS NOT NULL;
DROP INDEX work_narratives_one_successor;
CREATE INDEX work_narratives_successor ON work_narratives(supersedes_id) WHERE supersedes_id IS NOT NULL;

-- Resolutions now also settle supersession forks.
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

-- A supersession resolution rejects heads (record IDs of the subject's family,
-- the part of subject_id before the colon); accepted_id replaces each of them.
CREATE VIEW fork_rejected_records AS
SELECT f.project_id, substr(f.subject_id, 1, instr(f.subject_id, ':') - 1) AS family, CAST(j.value AS TEXT) AS id,
    f.accepted_head AS accepted_id, f.id AS resolution_id, f.recorded_at
FROM fork_resolutions f, json_each(f.rejected_heads_json) j
WHERE f.family = 'supersession';

-- A decision, commit narrative (evidence), or work narrative is no longer
-- current once a record supersedes it or a fork resolution rejects it; at is
-- when that was recorded.
CREATE VIEW superseded_records AS
SELECT project_id, 'decision' AS family, supersedes_id AS id, recorded_at AS at FROM decision_records WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, 'evidence', supersedes_id, recorded_at FROM evidence_records WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, 'narrative', supersedes_id, recorded_at FROM work_narratives WHERE supersedes_id IS NOT NULL
UNION ALL
SELECT project_id, family, id, recorded_at FROM fork_rejected_records;
