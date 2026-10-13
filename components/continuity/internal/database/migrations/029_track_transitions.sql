CREATE TABLE track_transitions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    operation TEXT NOT NULL CHECK(operation IN ('interrupt', 'return')),
    source_task_id TEXT NOT NULL,
    source_checkpoint_id TEXT NOT NULL REFERENCES checkpoints(id),
    target_task_id TEXT NOT NULL,
    target_checkpoint_id TEXT NOT NULL REFERENCES checkpoints(id),
    interruption_id TEXT REFERENCES track_transitions(id),
    source_state TEXT CHECK(source_state IS NULL OR source_state IN ('active', 'paused', 'superseded')),
    return_to_task_id TEXT,
    return_condition TEXT,
    idea_id TEXT,
    suggestion_id TEXT,
    reason TEXT NOT NULL,
    request_key TEXT NOT NULL,
    request_sha256 TEXT NOT NULL,
    authority TEXT NOT NULL CHECK(authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    CHECK(source_task_id <> target_task_id),
    CHECK(
        (operation = 'interrupt' AND interruption_id IS NULL AND source_state IS NOT NULL)
        OR
        (operation = 'return' AND interruption_id IS NOT NULL AND source_state IS NULL
            AND return_to_task_id IS NULL AND return_condition IS NULL
            AND idea_id IS NULL AND suggestion_id IS NULL)
    ),
    CHECK(return_condition IS NULL OR return_to_task_id IS NOT NULL),
    UNIQUE(project_id, request_key),
    FOREIGN KEY(project_id, suggestion_id) REFERENCES agent_suggestions(project_id, id)
) STRICT;

CREATE UNIQUE INDEX track_transitions_one_return
    ON track_transitions(project_id, interruption_id)
    WHERE operation = 'return';

CREATE INDEX track_transitions_project_source
    ON track_transitions(project_id, source_task_id, recorded_at, id);

CREATE INDEX track_transitions_project_target
    ON track_transitions(project_id, target_task_id, recorded_at, id);

CREATE INDEX track_transitions_project_return_target
    ON track_transitions(project_id, return_to_task_id, recorded_at, id)
    WHERE return_to_task_id IS NOT NULL;
