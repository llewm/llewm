CREATE TABLE action_policy_documents (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    action_type TEXT NOT NULL,
    policy_version TEXT NOT NULL,
    effect TEXT NOT NULL,
    prohibited_trailers_json TEXT NOT NULL,
    require_git_note INTEGER NOT NULL,
    source TEXT NOT NULL,
    source_version TEXT,
    content_hash TEXT NOT NULL,
    authority TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id)
) STRICT;

CREATE INDEX action_policy_documents_project_action_time
    ON action_policy_documents(project_id, action_type, observed_at DESC);
