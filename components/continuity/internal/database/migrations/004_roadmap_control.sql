CREATE TABLE roadmap_nodes (
    project_id TEXT NOT NULL REFERENCES projects(id),
    node_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    title TEXT NOT NULL,
    status TEXT NOT NULL,
    priority INTEGER NOT NULL,
    source_path TEXT NOT NULL,
    source_ref TEXT,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, node_id, revision)
) STRICT;

CREATE INDEX roadmap_nodes_project_status
    ON roadmap_nodes(project_id, status, priority, recorded_at DESC);

CREATE TABLE roadmap_file_bindings (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    node_id TEXT NOT NULL,
    node_revision INTEGER NOT NULL,
    path TEXT NOT NULL,
    path_kind TEXT NOT NULL,
    relationship TEXT NOT NULL,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(project_id, node_id, node_revision)
        REFERENCES roadmap_nodes(project_id, node_id, revision)
) STRICT;

CREATE INDEX roadmap_bindings_project_node
    ON roadmap_file_bindings(project_id, node_id, recorded_at DESC);

CREATE TABLE roadmap_sync_runs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    git_commit TEXT NOT NULL,
    client TEXT,
    model TEXT,
    invocation_id TEXT,
    reviewed_nodes_json TEXT NOT NULL,
    reviewed_files_json TEXT NOT NULL,
    authority TEXT NOT NULL,
    git_authority TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(invocation_id, project_id) REFERENCES actor_invocations(id, project_id)
) STRICT;

CREATE INDEX roadmap_sync_runs_project_time
    ON roadmap_sync_runs(project_id, completed_at DESC);

CREATE TABLE roadmap_sync_files (
    sync_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    path TEXT NOT NULL,
    content_hash TEXT,
    state TEXT NOT NULL,
    PRIMARY KEY(sync_id, path),
    FOREIGN KEY(sync_id, project_id) REFERENCES roadmap_sync_runs(id, project_id)
) STRICT;
