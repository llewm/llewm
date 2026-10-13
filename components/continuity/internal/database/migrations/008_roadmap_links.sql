CREATE TABLE roadmap_node_links (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    node_id TEXT NOT NULL,
    node_revision INTEGER NOT NULL,
    target_node_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(project_id, node_id, node_revision)
        REFERENCES roadmap_nodes(project_id, node_id, revision)
) STRICT;

CREATE INDEX roadmap_node_links_project_node
    ON roadmap_node_links(project_id, node_id, node_revision, recorded_at DESC);
