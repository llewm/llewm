CREATE TABLE autonext_codex_bindings (
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK(schema_version=1),
    PRIMARY KEY(project_id, run_id),
    UNIQUE(project_id, run_id, session_id),
    FOREIGN KEY(project_id, run_id) REFERENCES autonext_runs(project_id, id)
) STRICT;

CREATE TABLE autonext_codex_claims (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    completed_steps INTEGER NOT NULL CHECK(completed_steps > 0),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK(schema_version=1),
    UNIQUE(project_id, run_id, completed_steps),
    UNIQUE(project_id, session_id, turn_id),
    FOREIGN KEY(project_id, run_id, session_id) REFERENCES autonext_codex_bindings(project_id, run_id, session_id)
) STRICT;

CREATE TABLE autonext_codex_prompt_receipts (
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('initial','continuation','human_input')),
    claim_id TEXT,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK(schema_version=1),
    PRIMARY KEY(project_id, run_id, session_id, turn_id),
    FOREIGN KEY(project_id, run_id, session_id) REFERENCES autonext_codex_bindings(project_id, run_id, session_id),
    FOREIGN KEY(claim_id) REFERENCES autonext_codex_claims(id)
) STRICT;

CREATE UNIQUE INDEX autonext_codex_one_initial_prompt ON autonext_codex_prompt_receipts(project_id, run_id) WHERE kind='initial';
