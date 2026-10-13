CREATE TABLE reviewed_procedures (
    project_id TEXT NOT NULL REFERENCES projects(id),
    procedure_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    purpose TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind = 'completion_check'),
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

CREATE INDEX reviewed_procedures_project_status
    ON reviewed_procedures(project_id, status, procedure_id, revision DESC);

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

CREATE INDEX completion_profile_procedure_bindings_procedure
    ON completion_profile_procedure_bindings(project_id, procedure_id, procedure_revision);
