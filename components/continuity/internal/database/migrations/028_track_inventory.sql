CREATE TABLE roadmap_task_bindings (
    project_id TEXT NOT NULL REFERENCES projects(id),
    roadmap_node_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0),
    roadmap_node_revision INTEGER NOT NULL CHECK(roadmap_node_revision > 0),
    status TEXT NOT NULL CHECK(status IN ('active', 'superseded')),
    reason TEXT NOT NULL,
    request_key TEXT NOT NULL,
    request_sha256 TEXT NOT NULL,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, roadmap_node_id, task_id, revision),
    UNIQUE(project_id, request_key),
    FOREIGN KEY(project_id, roadmap_node_id, roadmap_node_revision)
        REFERENCES roadmap_nodes(project_id, node_id, revision)
) STRICT;

CREATE INDEX roadmap_task_bindings_current
    ON roadmap_task_bindings(project_id, roadmap_node_id, task_id, revision DESC);

CREATE INDEX roadmap_task_bindings_task
    ON roadmap_task_bindings(project_id, task_id, roadmap_node_id, revision DESC);
