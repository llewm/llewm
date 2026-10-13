CREATE TABLE roadmap_releases (
    project_id TEXT NOT NULL REFERENCES projects(id),
    release_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    version TEXT NOT NULL,
    title TEXT NOT NULL,
    status TEXT NOT NULL,
    reviewer TEXT,
    published_at TEXT,
    content_sha256 TEXT NOT NULL,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY (project_id, release_id, revision)
) STRICT;

CREATE TABLE roadmap_release_entries (
    project_id TEXT NOT NULL,
    release_id TEXT NOT NULL,
    release_revision INTEGER NOT NULL,
    position INTEGER NOT NULL,
    category TEXT NOT NULL,
    wording TEXT NOT NULL,
    node_id TEXT NOT NULL,
    node_revision INTEGER NOT NULL,
    narrative_ids_json TEXT NOT NULL,
    PRIMARY KEY (project_id, release_id, release_revision, position),
    FOREIGN KEY (project_id, release_id, release_revision)
        REFERENCES roadmap_releases(project_id, release_id, revision)
) STRICT;

CREATE INDEX roadmap_releases_project_recorded
    ON roadmap_releases(project_id, recorded_at DESC, release_id, revision DESC);

CREATE INDEX roadmap_release_entries_node
    ON roadmap_release_entries(project_id, node_id, node_revision);
