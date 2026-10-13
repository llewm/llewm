-- Device-local advisory agent presence. Presence is deliberately distinct
-- from durable tasks and four-hour coordination leases: short-lived claims say
-- which client session is plausibly active in one checkout right now. These
-- rows never enter portable memory.

CREATE TABLE presence_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    claim_id TEXT NOT NULL CHECK (claim_id <> ''),
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    actor TEXT NOT NULL CHECK (actor <> ''),
    client TEXT NOT NULL CHECK (client <> ''),
    session_id TEXT NOT NULL CHECK (session_id <> ''),
    invocation_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    roadmap_node_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('planning', 'active', 'yielded', 'completed', 'cancelled', 'expired')),
    started_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('machine_observed', 'caller_asserted', 'user_reported')),
    checkout_path TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    branch TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE (project_id, claim_id, sequence)
) STRICT;

CREATE INDEX presence_events_session
    ON presence_events(project_id, session_id, observed_at DESC);
CREATE INDEX presence_events_task
    ON presence_events(project_id, task_id, observed_at DESC);
CREATE INDEX presence_events_checkout
    ON presence_events(project_id, checkout_path, worktree_path, branch, observed_at DESC);

CREATE TRIGGER presence_events_no_update BEFORE UPDATE ON presence_events
BEGIN SELECT RAISE(ABORT, 'presence events are append-only'); END;
CREATE TRIGGER presence_events_no_delete BEFORE DELETE ON presence_events
BEGIN SELECT RAISE(ABORT, 'presence events are append-only'); END;
