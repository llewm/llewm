-- Fencing (5b of continuity-multi-repo-sync): each checkpoint and plan revision
-- written against a scope records which lease it was written under. A leased
-- stamp names the scope and the token of the holder's current grant; an
-- unleased stamp means no lease was presented, and a lapsed stamp means the
-- writer presented a token that was no longer the scope's current, unexpired
-- grant (its lease was lost). Required scopes refuse such writes instead, so
-- only advisory scopes carry unleased and lapsed stamps. Stamps are
-- device-local and append-only; they do not travel yet.
CREATE TABLE lease_stamps (
    project_id TEXT NOT NULL REFERENCES projects(id),
    record_kind TEXT NOT NULL CHECK (record_kind IN ('checkpoint', 'track_plan_revision')),
    record_id TEXT NOT NULL CHECK (record_id <> ''),
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('task', 'roadmap_node', 'track_plan')),
    scope_id TEXT NOT NULL CHECK (scope_id <> ''),
    state TEXT NOT NULL CHECK (state IN ('leased', 'unleased', 'lapsed')),
    token INTEGER CHECK (token IS NULL OR token >= 1),
    holder TEXT NOT NULL,
    session_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, record_kind, record_id),
    CHECK ((state = 'unleased') = (token IS NULL))
) STRICT;

CREATE TRIGGER lease_stamps_no_update BEFORE UPDATE ON lease_stamps BEGIN SELECT RAISE(ABORT,'lease stamps are append-only'); END;
CREATE TRIGGER lease_stamps_no_delete BEFORE DELETE ON lease_stamps BEGIN SELECT RAISE(ABORT,'lease stamps are append-only'); END;
