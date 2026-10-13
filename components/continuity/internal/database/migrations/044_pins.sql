-- A pin asks the bootstrap to surface a record when its scope is relevant. Pins
-- are append-only assertions (pin or unpin); the latest action for a target and
-- scope wins. Pinning never changes the pinned record.

CREATE TABLE pins (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('resource', 'procedure', 'decision', 'topic')),
    target_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('project', 'topic', 'roadmap_node', 'task')),
    scope_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('pin', 'unpin')),
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    CHECK ((scope_kind = 'project') = (scope_id = ''))
) STRICT;

CREATE INDEX pins_scope ON pins(project_id, scope_kind, scope_id, target_kind, target_id, recorded_at DESC, id DESC);
