-- Standing grants (6b of continuity-multi-repo-sync): a person lets agents run
-- a named procedure revision on a trigger (after a slice commit) without
-- asking each time, until the grant expires or is revoked. The grant covers
-- the procedure and every procedure its steps name (procedure:ID), each at the
-- exact revision current when the grant was made; a newer revision of any of
-- them voids the grant. Continuity executes nothing: the agent runs the steps.
-- Grants are local to this machine, like leases.

CREATE TABLE standing_grants (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    procedure_id TEXT NOT NULL CHECK (procedure_id <> ''),
    procedure_revision INTEGER NOT NULL CHECK (procedure_revision >= 1),
    trigger_kind TEXT NOT NULL CHECK (trigger_kind <> ''),
    parameters_json TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    creator TEXT NOT NULL CHECK (creator <> ''),
    reason TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'user_reported'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    FOREIGN KEY (project_id, procedure_id, procedure_revision) REFERENCES reviewed_procedures(project_id, procedure_id, revision)
) STRICT;

CREATE INDEX standing_grants_project ON standing_grants(project_id, recorded_at DESC);

-- The procedures a grant covers: position 0 is the granted procedure itself,
-- later positions the procedures its steps name, in first-mention order.
CREATE TABLE standing_grant_procedures (
    grant_id TEXT NOT NULL REFERENCES standing_grants(id),
    position INTEGER NOT NULL CHECK (position >= 0),
    project_id TEXT NOT NULL,
    procedure_id TEXT NOT NULL CHECK (procedure_id <> ''),
    procedure_revision INTEGER NOT NULL CHECK (procedure_revision >= 1),
    PRIMARY KEY (grant_id, position),
    UNIQUE (grant_id, procedure_id),
    FOREIGN KEY (project_id, procedure_id, procedure_revision) REFERENCES reviewed_procedures(project_id, procedure_id, revision)
) STRICT;

-- A revocation ends a grant for good; there is at most one per grant.
CREATE TABLE standing_grant_revocations (
    grant_id TEXT PRIMARY KEY REFERENCES standing_grants(id),
    creator TEXT NOT NULL CHECK (creator <> ''),
    reason TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;
