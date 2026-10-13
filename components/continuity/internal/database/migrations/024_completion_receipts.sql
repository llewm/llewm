CREATE UNIQUE INDEX checkpoints_project_id_id
    ON checkpoints(project_id, id);

CREATE TABLE completion_stage_receipts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL,
    checkpoint_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL CHECK (profile_revision > 0),
    stage_order INTEGER NOT NULL CHECK (stage_order > 0),
    stage_kind TEXT NOT NULL CHECK (stage_kind IN ('source_check', 'commit_disposition', 'roadmap_disposition', 'output_adapter', 'terminal_checkpoint')),
    stage_id TEXT NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('succeeded', 'failed')),
    outcome TEXT NOT NULL CHECK (outcome IN ('passed', 'committed', 'no_commit', 'synced', 'no_sync', 'observed', 'projected', 'externally_satisfied', 'verified', 'failed')),
    detail TEXT,
    source TEXT NOT NULL,
    source_ref TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    FOREIGN KEY (project_id, checkpoint_id) REFERENCES checkpoints(project_id, id),
    FOREIGN KEY (project_id, profile_id, profile_revision) REFERENCES completion_profiles(project_id, profile_id, revision),
    UNIQUE (project_id, request_key)
) STRICT;

CREATE INDEX completion_stage_receipts_run
    ON completion_stage_receipts(project_id, task_id, checkpoint_id, profile_id, profile_revision, stage_order, recorded_at, id);
