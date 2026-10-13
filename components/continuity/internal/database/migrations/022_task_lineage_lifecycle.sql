ALTER TABLE checkpoints ADD COLUMN request_key TEXT;
ALTER TABLE checkpoints ADD COLUMN request_hash TEXT;

CREATE UNIQUE INDEX checkpoints_project_request_key
    ON checkpoints(project_id, request_key)
    WHERE request_key IS NOT NULL;

CREATE TABLE task_lineage_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    operation TEXT NOT NULL CHECK (operation IN ('start', 'fork', 'rekey', 'supersede', 'abandon')),
    task_id TEXT NOT NULL,
    target_task_id TEXT,
    checkpoint_id TEXT REFERENCES checkpoints(id),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    reason TEXT,
    created_at TEXT NOT NULL,
    CHECK (
        (operation IN ('fork', 'rekey', 'supersede') AND target_task_id IS NOT NULL)
        OR (operation IN ('start', 'abandon') AND target_task_id IS NULL)
    ),
    CHECK (target_task_id IS NULL OR target_task_id <> task_id),
    UNIQUE(project_id, request_key)
) STRICT;

CREATE UNIQUE INDEX task_lineage_one_start
    ON task_lineage_events(project_id, task_id)
    WHERE operation = 'start';

CREATE UNIQUE INDEX task_lineage_one_created_target
    ON task_lineage_events(project_id, target_task_id)
    WHERE operation IN ('fork', 'rekey');

CREATE UNIQUE INDEX task_lineage_one_terminal
    ON task_lineage_events(project_id, task_id)
    WHERE operation IN ('rekey', 'supersede', 'abandon');

CREATE INDEX task_lineage_project_task
    ON task_lineage_events(project_id, task_id, created_at, id);

CREATE INDEX task_lineage_project_target
    ON task_lineage_events(project_id, target_task_id, created_at, id);
