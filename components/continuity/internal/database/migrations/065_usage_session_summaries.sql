-- Usage session summaries (8c of continuity-auditing). Raw per-request usage
-- stays on the machine that ran the session; a summary of each finished
-- session travels through History sync so reports cover every machine. Each
-- summary is immutable and named by its content hash, so a resumed session
-- simply adds a newer summary; readers keep the newest per session. Rows here
-- are summaries imported from the memory repository (this machine's own are
-- derived from usage_requests when exported).
CREATE TABLE usage_session_summaries (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    client TEXT NOT NULL,
    session_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    task_id TEXT NOT NULL,
    overhead_tokens INTEGER NOT NULL CHECK (overhead_tokens >= 0),
    models_json TEXT NOT NULL,
    tools_json TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id)
) STRICT;
CREATE INDEX usage_session_summaries_session ON usage_session_summaries(project_id, client, session_id, last_at);
