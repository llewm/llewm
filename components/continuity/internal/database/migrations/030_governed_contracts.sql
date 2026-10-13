CREATE TABLE governed_contract_revisions (
    project_id TEXT NOT NULL REFERENCES projects(id),
    contract_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0),
    title TEXT NOT NULL,
    purpose TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('client_lifecycle', 'adapter_interchange', 'portability', 'data_retention_redaction', 'completion_workflow', 'operational_qualification')),
    scope TEXT NOT NULL,
    initial_status TEXT NOT NULL CHECK(initial_status IN ('draft', 'proposed', 'accepted', 'active', 'superseded', 'retired')),
    selectors_json TEXT NOT NULL,
    clauses_json TEXT NOT NULL,
    dependencies_json TEXT NOT NULL,
    incompatibilities_json TEXT NOT NULL,
    supersedes_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    reviewer TEXT,
    effective_at TEXT,
    review_condition TEXT,
    expiry_condition TEXT,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, contract_id, revision)
) STRICT;

CREATE INDEX governed_contract_revisions_list
    ON governed_contract_revisions(project_id, recorded_at DESC, contract_id, revision DESC);

CREATE TABLE governed_contract_source_bindings (
    project_id TEXT NOT NULL,
    contract_id TEXT NOT NULL,
    contract_revision INTEGER NOT NULL,
    position INTEGER NOT NULL CHECK(position >= 0),
    path TEXT NOT NULL,
    section TEXT,
    source_sha256 TEXT NOT NULL,
    relationship TEXT NOT NULL CHECK(relationship IN ('definition', 'rationale', 'acceptance', 'example')),
    PRIMARY KEY(project_id, contract_id, contract_revision, position),
    UNIQUE(project_id, contract_id, contract_revision, path, section, relationship),
    FOREIGN KEY(project_id, contract_id, contract_revision)
        REFERENCES governed_contract_revisions(project_id, contract_id, revision)
) STRICT;

CREATE INDEX governed_contract_source_bindings_path
    ON governed_contract_source_bindings(project_id, path, contract_id, contract_revision);

CREATE TABLE governed_contract_transitions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    contract_id TEXT NOT NULL,
    contract_revision INTEGER NOT NULL,
    sequence INTEGER NOT NULL CHECK(sequence > 0),
    from_status TEXT NOT NULL CHECK(from_status IN ('draft', 'proposed', 'accepted', 'active', 'superseded', 'retired')),
    to_status TEXT NOT NULL CHECK(to_status IN ('draft', 'proposed', 'accepted', 'active', 'superseded', 'retired')),
    reason TEXT NOT NULL,
    creator TEXT NOT NULL,
    reviewer TEXT,
    request_key TEXT NOT NULL,
    request_sha256 TEXT NOT NULL,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    UNIQUE(project_id, request_key),
    UNIQUE(project_id, contract_id, contract_revision, sequence),
    FOREIGN KEY(project_id, contract_id, contract_revision)
        REFERENCES governed_contract_revisions(project_id, contract_id, revision)
) STRICT;

CREATE INDEX governed_contract_transitions_history
    ON governed_contract_transitions(project_id, contract_id, contract_revision, sequence);
