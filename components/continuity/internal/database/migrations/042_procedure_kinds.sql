-- Procedures gain the runbook kind, and completion_check is renamed check.
-- SQLite cannot alter a CHECK constraint, so both procedure tables are rebuilt.
-- Bindings are parked in a temporary table first, so no foreign key refers to a
-- table while it is dropped.

CREATE TEMP TABLE procedure_bindings_parked AS
SELECT * FROM completion_profile_procedure_bindings;

DROP TABLE completion_profile_procedure_bindings;

CREATE TABLE reviewed_procedures_next (
    project_id TEXT NOT NULL REFERENCES projects(id),
    procedure_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    purpose TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('check', 'runbook')),
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'superseded')),
    platforms_json TEXT NOT NULL,
    working_directory TEXT NOT NULL,
    parameters_json TEXT NOT NULL,
    declared_effects_json TEXT NOT NULL,
    validations_json TEXT NOT NULL,
    post_run_checks_json TEXT NOT NULL,
    steps_json TEXT NOT NULL,
    source TEXT NOT NULL,
    source_ref TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, procedure_id, revision)
) STRICT;

INSERT INTO reviewed_procedures_next(
    project_id, procedure_id, revision, title, purpose, kind, status, platforms_json,
    working_directory, parameters_json, declared_effects_json, validations_json,
    post_run_checks_json, steps_json, source, source_ref, authority, recorded_at, schema_version
)
SELECT
    project_id, procedure_id, revision, title, purpose,
    CASE kind WHEN 'completion_check' THEN 'check' ELSE kind END,
    status, platforms_json, working_directory, parameters_json, declared_effects_json,
    validations_json, post_run_checks_json, steps_json, source, source_ref, authority,
    recorded_at, schema_version
FROM reviewed_procedures;

DROP TABLE reviewed_procedures;
ALTER TABLE reviewed_procedures_next RENAME TO reviewed_procedures;

CREATE INDEX reviewed_procedures_project_status
    ON reviewed_procedures(project_id, status, procedure_id, revision DESC);
CREATE INDEX reviewed_procedures_project_kind
    ON reviewed_procedures(project_id, kind, procedure_id, revision DESC);

CREATE TABLE completion_profile_procedure_bindings (
    project_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL CHECK (profile_revision > 0),
    binding_order INTEGER NOT NULL CHECK (binding_order > 0),
    procedure_id TEXT NOT NULL,
    procedure_revision INTEGER NOT NULL CHECK (procedure_revision > 0),
    stage_ids_json TEXT NOT NULL,
    PRIMARY KEY (project_id, profile_id, profile_revision, procedure_id, procedure_revision),
    UNIQUE (project_id, profile_id, profile_revision, binding_order),
    FOREIGN KEY (project_id, profile_id, profile_revision)
        REFERENCES completion_profiles(project_id, profile_id, revision),
    FOREIGN KEY (project_id, procedure_id, procedure_revision)
        REFERENCES reviewed_procedures(project_id, procedure_id, revision)
) STRICT;

INSERT INTO completion_profile_procedure_bindings
SELECT * FROM procedure_bindings_parked;
DROP TABLE procedure_bindings_parked;

CREATE INDEX completion_profile_procedure_bindings_procedure
    ON completion_profile_procedure_bindings(project_id, procedure_id, procedure_revision);

-- Procedures join history search: the purpose, then each step description with
-- its inert argv template, so a runbook can be found by the command it uses.
CREATE TRIGGER reviewed_procedures_index AFTER INSERT ON reviewed_procedures
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT NEW.title,
        NEW.purpose || COALESCE(char(10) || (SELECT group_concat(json_extract(s.value, '$.description') || COALESCE(': ' || (SELECT group_concat(a.value, ' ') FROM json_each(s.value, '$.argv_template') a), ''), char(10)) FROM json_each(NEW.steps_json) s), ''),
        NEW.project_id, 'procedure', 'procedure:' || NEW.procedure_id || '@' || NEW.revision, NEW.kind, NEW.recorded_at;
END;

INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
SELECT p.title,
    p.purpose || COALESCE(char(10) || (SELECT group_concat(json_extract(s.value, '$.description') || COALESCE(': ' || (SELECT group_concat(a.value, ' ') FROM json_each(s.value, '$.argv_template') a), ''), char(10)) FROM json_each(p.steps_json) s), ''),
    p.project_id, 'procedure', 'procedure:' || p.procedure_id || '@' || p.revision, p.kind, p.recorded_at
FROM reviewed_procedures p;

-- Memory-sync records whose type this Continuity cannot read yet are skipped and
-- counted, not refused, so a machine on an older build keeps importing the rest.
ALTER TABLE sync_attempts ADD COLUMN deferred_records INTEGER NOT NULL DEFAULT 0 CHECK (deferred_records >= 0);
