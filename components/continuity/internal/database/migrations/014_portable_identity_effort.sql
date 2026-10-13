CREATE TABLE project_memory_identities (
    project_id TEXT PRIMARY KEY REFERENCES projects(id),
    lineage_id TEXT NOT NULL,
    instance_id TEXT NOT NULL UNIQUE,
    association_source TEXT NOT NULL CHECK (association_source IN ('generated', 'explicit_adoption')),
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    associated_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX project_memory_identities_lineage
    ON project_memory_identities(lineage_id, associated_at, project_id);

ALTER TABLE actor_invocations ADD COLUMN effort_requested_raw TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_requested_normalized TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_requested_source TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_requested_authority TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_effective_raw TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_effective_normalized TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_effective_source TEXT;
ALTER TABLE actor_invocations ADD COLUMN effort_effective_authority TEXT;
