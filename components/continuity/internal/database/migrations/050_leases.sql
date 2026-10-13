-- Local leases (5a of continuity-multi-repo-sync): parallel agents on one
-- machine say who is working on a task, roadmap node, or track plan. A grant
-- is one holder's hold on a scope under a token that only increases per
-- scope; its events (acquire, renew, release) are append-only and the latest
-- one says when it expires or that it was released. Leases are device-local
-- and advisory: nothing refuses work because of them.

CREATE TABLE lease_grants (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('task', 'roadmap_node', 'track_plan')),
    scope_id TEXT NOT NULL CHECK (scope_id <> ''),
    token INTEGER NOT NULL CHECK (token >= 1),
    holder TEXT NOT NULL CHECK (holder <> ''),
    session_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE (project_id, scope_kind, scope_id, token)
) STRICT;

CREATE TABLE lease_events (
    id TEXT PRIMARY KEY,
    grant_id TEXT NOT NULL REFERENCES lease_grants(id),
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    action TEXT NOT NULL CHECK (action IN ('acquire', 'renew', 'release')),
    expires_at TEXT NOT NULL,
    margin_ms INTEGER NOT NULL CHECK (margin_ms >= 0),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE (grant_id, sequence),
    CHECK ((action = 'acquire') = (sequence = 1))
) STRICT;
