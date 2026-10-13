CREATE TABLE actor_invocations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    session_id TEXT,
    parent_invocation_id TEXT,
    actor_type TEXT NOT NULL,
    client TEXT,
    client_version TEXT,
    provider TEXT,
    model TEXT,
    identity_source TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(parent_invocation_id, project_id) REFERENCES actor_invocations(id, project_id)
) STRICT;

CREATE INDEX actor_invocations_project_time ON actor_invocations(project_id, occurred_at DESC);
CREATE INDEX actor_invocations_session ON actor_invocations(project_id, session_id);

CREATE TABLE policy_references (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    source TEXT NOT NULL,
    source_version TEXT,
    content_hash TEXT NOT NULL,
    authority TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id)
) STRICT;

CREATE INDEX policy_references_project_time ON policy_references(project_id, observed_at DESC);

CREATE TABLE client_capability_evidence (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    client_version TEXT,
    capability TEXT NOT NULL,
    support_status TEXT NOT NULL,
    scope_precedence TEXT,
    source_url TEXT,
    source_date TEXT,
    verification_status TEXT NOT NULL,
    evidence TEXT,
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id)
) STRICT;

CREATE INDEX client_capability_project_time ON client_capability_evidence(project_id, client, observed_at DESC);

CREATE TABLE instruction_incidents (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    performer_invocation_id TEXT,
    reporter_invocation_id TEXT,
    policy_reference_id TEXT,
    capability_evidence_id TEXT,
    incident_type TEXT NOT NULL,
    requested_action TEXT NOT NULL,
    conflict_statement TEXT,
    evidence_ref TEXT,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(performer_invocation_id, project_id) REFERENCES actor_invocations(id, project_id),
    FOREIGN KEY(reporter_invocation_id, project_id) REFERENCES actor_invocations(id, project_id),
    FOREIGN KEY(policy_reference_id, project_id) REFERENCES policy_references(id, project_id),
    FOREIGN KEY(capability_evidence_id, project_id) REFERENCES client_capability_evidence(id, project_id)
) STRICT;

CREATE INDEX instruction_incidents_project_time ON instruction_incidents(project_id, occurred_at DESC);

CREATE TABLE incident_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    incident_id TEXT NOT NULL,
    reviewer_invocation_id TEXT,
    transition TEXT NOT NULL,
    detail TEXT,
    evidence_ref TEXT,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    FOREIGN KEY(incident_id, project_id) REFERENCES instruction_incidents(id, project_id),
    FOREIGN KEY(reviewer_invocation_id, project_id) REFERENCES actor_invocations(id, project_id)
) STRICT;

CREATE INDEX incident_events_incident_time ON incident_events(project_id, incident_id, occurred_at);

CREATE TABLE action_restrictions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    incident_id TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_ref TEXT NOT NULL,
    action_type TEXT NOT NULL,
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(id, project_id),
    FOREIGN KEY(incident_id, project_id) REFERENCES instruction_incidents(id, project_id)
) STRICT;

CREATE INDEX action_restrictions_scope ON action_restrictions(project_id, scope_type, scope_ref, created_at DESC);

CREATE TABLE restriction_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    restriction_id TEXT NOT NULL,
    reviewer_invocation_id TEXT,
    transition TEXT NOT NULL,
    detail TEXT,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    FOREIGN KEY(restriction_id, project_id) REFERENCES action_restrictions(id, project_id),
    FOREIGN KEY(reviewer_invocation_id, project_id) REFERENCES actor_invocations(id, project_id)
) STRICT;

CREATE INDEX restriction_events_restriction_time ON restriction_events(project_id, restriction_id, occurred_at);
