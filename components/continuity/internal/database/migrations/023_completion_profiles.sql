CREATE TABLE completion_profiles (
    project_id TEXT NOT NULL REFERENCES projects(id),
    profile_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'superseded')),
    required_checks_json TEXT NOT NULL,
    adapters_json TEXT NOT NULL,
    source TEXT NOT NULL,
    source_ref TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, profile_id, revision)
) STRICT;

CREATE INDEX completion_profiles_project_status
    ON completion_profiles(project_id, status, profile_id, revision DESC);
