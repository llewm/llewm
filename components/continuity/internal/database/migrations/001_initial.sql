CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    root TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    model TEXT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    status TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    title TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    session_id TEXT,
    task_id TEXT,
    client TEXT NOT NULL,
    event_type TEXT NOT NULL,
    category TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_model TEXT,
    outcome TEXT,
    duration_ms INTEGER,
    payload_json TEXT,
    schema_version INTEGER NOT NULL,
    content_hash TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS events_project_time ON events(project_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS events_session_time ON events(session_id, occurred_at);
CREATE INDEX IF NOT EXISTS events_type_time ON events(event_type, occurred_at DESC);

CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    session_id TEXT,
    task_title TEXT NOT NULL,
    status TEXT NOT NULL,
    summary TEXT NOT NULL,
    next_action TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS checkpoints_project_time ON checkpoints(project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS rule_sections (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    source_path TEXT NOT NULL,
    heading_path TEXT NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    precedence INTEGER NOT NULL,
    active INTEGER NOT NULL,
    observed_at TEXT NOT NULL,
    UNIQUE(project_id, source_path, heading_path, content_hash)
) STRICT;

CREATE TABLE IF NOT EXISTS environment_facts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    fact TEXT NOT NULL,
    authority TEXT NOT NULL,
    evidence_ref TEXT,
    status TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT
) STRICT;

CREATE TABLE IF NOT EXISTS usage_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    session_id TEXT,
    event_id TEXT,
    provider TEXT,
    model TEXT,
    input_tokens INTEGER,
    cached_input_tokens INTEGER,
    output_tokens INTEGER,
    reasoning_tokens INTEGER,
    estimated_cost_microunits INTEGER,
    measurement_source TEXT NOT NULL,
    confidence TEXT NOT NULL,
    occurred_at TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS procedure_candidates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL,
    normalized_sequence_json TEXT NOT NULL,
    observation_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    score REAL NOT NULL,
    status TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
) STRICT;
