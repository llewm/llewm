CREATE TABLE roadmap_authority_migrations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    projection_id TEXT NOT NULL,
    previous_mode TEXT NOT NULL CHECK(previous_mode IN ('repository', 'llmem')),
    target_mode TEXT NOT NULL CHECK(target_mode IN ('repository', 'llmem')),
    plan_sha256 TEXT NOT NULL,
    graph_sha256 TEXT NOT NULL,
    renderer_name TEXT NOT NULL,
    renderer_version INTEGER NOT NULL,
    renderer_sha256 TEXT NOT NULL,
    creator TEXT NOT NULL,
    creator_invocation_id TEXT,
    review_authority TEXT NOT NULL,
    file_authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    payload_json TEXT NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(creator_invocation_id, project_id)
        REFERENCES actor_invocations(id, project_id)
) STRICT;

CREATE INDEX roadmap_authority_migrations_active
    ON roadmap_authority_migrations(project_id, projection_id, recorded_at DESC, id DESC);

CREATE TABLE roadmap_authority_sources (
    project_id TEXT NOT NULL,
    migration_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    path TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK(source_kind IN ('hand_authored', 'adopted_projection')),
    content_sha256 TEXT NOT NULL,
    PRIMARY KEY(project_id, migration_id, ordinal),
    FOREIGN KEY(migration_id, project_id)
        REFERENCES roadmap_authority_migrations(id, project_id)
) STRICT;

CREATE TABLE roadmap_authority_destinations (
    project_id TEXT NOT NULL,
    migration_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    path TEXT NOT NULL,
    prewrite_state TEXT NOT NULL CHECK(prewrite_state IN ('missing', 'current', 'drifted')),
    prewrite_sha256 TEXT,
    projection_sha256 TEXT NOT NULL,
    PRIMARY KEY(project_id, migration_id, ordinal),
    FOREIGN KEY(migration_id, project_id)
        REFERENCES roadmap_authority_migrations(id, project_id)
) STRICT;

CREATE TRIGGER roadmap_authority_invocation_project_insert
BEFORE INSERT ON roadmap_authority_migrations
WHEN NEW.creator_invocation_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM actor_invocations
    WHERE id = NEW.creator_invocation_id AND project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'roadmap authority invocation must belong to the same project');
END;
