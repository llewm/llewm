CREATE TABLE autonext_grants (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL,
    plan_ref TEXT,
    creator TEXT NOT NULL,
    creator_invocation_id TEXT REFERENCES actor_invocations(id),
    source_checkpoint_id TEXT,
    permitted_actions_json TEXT NOT NULL,
    excluded_actions_json TEXT NOT NULL,
    max_steps INTEGER NOT NULL CHECK (max_steps > 0),
    review_every INTEGER NOT NULL CHECK (review_every >= 0),
    expires_at TEXT,
    max_elapsed_seconds INTEGER NOT NULL CHECK (max_elapsed_seconds >= 0),
    budget_limits_json TEXT NOT NULL,
    required_verifications_json TEXT NOT NULL,
    stop_policy TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, id)
) STRICT;

CREATE TABLE autonext_grant_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    grant_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('revoked', 'superseded')),
    reason TEXT NOT NULL,
    replacement_grant_id TEXT,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, grant_id),
    FOREIGN KEY(project_id, grant_id) REFERENCES autonext_grants(project_id, id)
) STRICT;

CREATE TABLE autonext_runs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    grant_id TEXT NOT NULL,
    client TEXT NOT NULL,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted')),
    started_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, id),
    UNIQUE(project_id, grant_id),
    FOREIGN KEY(project_id, grant_id) REFERENCES autonext_grants(project_id, id)
) STRICT;

CREATE TABLE autonext_run_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('yielded', 'completed', 'failed')),
    reason TEXT NOT NULL,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, run_id),
    FOREIGN KEY(project_id, run_id) REFERENCES autonext_runs(project_id, id)
) STRICT;

CREATE TABLE autonext_step_transitions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    step_number INTEGER NOT NULL CHECK (step_number > 0),
    state TEXT NOT NULL CHECK (state IN ('started', 'completed', 'failed', 'yielded', 'skipped')),
    interaction_class TEXT NOT NULL CHECK (interaction_class IN ('auto_eligible', 'review', 'input', 'authorization')),
    action_class TEXT NOT NULL,
    summary TEXT NOT NULL,
    verifications_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, run_id, step_number, state),
    FOREIGN KEY(project_id, run_id) REFERENCES autonext_runs(project_id, id)
) STRICT;

CREATE TABLE autonext_budget_observations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    metric TEXT NOT NULL CHECK (metric IN ('tokens', 'cost', 'tool_calls', 'changed_files')),
    amount INTEGER NOT NULL CHECK (amount >= 0),
    unit TEXT NOT NULL,
    period TEXT NOT NULL,
    source TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('provider_reported', 'host_observed', 'caller_asserted', 'estimated')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, run_id, id),
    FOREIGN KEY(project_id, run_id) REFERENCES autonext_runs(project_id, id)
) STRICT;

CREATE INDEX autonext_grants_project_time ON autonext_grants(project_id, recorded_at DESC, id DESC);
CREATE INDEX autonext_runs_project_time ON autonext_runs(project_id, started_at DESC, id DESC);
CREATE INDEX autonext_steps_run_order ON autonext_step_transitions(project_id, run_id, step_number, recorded_at, id);
CREATE INDEX autonext_budgets_run_metric ON autonext_budget_observations(project_id, run_id, metric, recorded_at, id);

CREATE TRIGGER autonext_grants_invocation_project_insert
BEFORE INSERT ON autonext_grants
WHEN NEW.creator_invocation_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM actor_invocations
    WHERE id = NEW.creator_invocation_id AND project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'creator invocation must belong to grant project');
END;

CREATE TRIGGER autonext_grant_supersession_target_insert
BEFORE INSERT ON autonext_grant_events
WHEN NEW.event_type = 'superseded'
 AND (NEW.replacement_grant_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM autonext_grants
    WHERE id = NEW.replacement_grant_id AND project_id = NEW.project_id
 ))
BEGIN
    SELECT RAISE(ABORT, 'replacement grant must belong to grant project');
END;
