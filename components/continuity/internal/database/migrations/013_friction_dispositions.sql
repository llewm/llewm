CREATE TABLE friction_dispositions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    candidate_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('investigate', 'ignore', 'resolved', 'snoozed')),
    reason TEXT NOT NULL,
    snoozed_until TEXT,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    FOREIGN KEY(project_id) REFERENCES projects(id)
) STRICT;

CREATE INDEX friction_dispositions_project_candidate_time
    ON friction_dispositions(project_id, candidate_id, recorded_at DESC, id DESC);
