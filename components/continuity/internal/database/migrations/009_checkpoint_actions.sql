CREATE TABLE checkpoint_actions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    source_checkpoint_id TEXT NOT NULL REFERENCES checkpoints(id),
    description TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE checkpoint_action_transitions (
    id TEXT PRIMARY KEY,
    action_id TEXT NOT NULL REFERENCES checkpoint_actions(id),
    checkpoint_id TEXT REFERENCES checkpoints(id),
    status TEXT NOT NULL CHECK (status IN ('open', 'carried_forward', 'completed', 'superseded')),
    reason TEXT,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX checkpoint_actions_project_time
    ON checkpoint_actions(project_id, created_at DESC, id DESC);

CREATE INDEX checkpoint_action_transitions_action_time
    ON checkpoint_action_transitions(action_id, created_at DESC, id DESC);

INSERT INTO checkpoint_actions(id, project_id, source_checkpoint_id, description, created_at)
SELECT 'action-' || id, project_id, id, next_action, created_at
FROM checkpoints
WHERE status <> 'completed';

INSERT INTO checkpoint_action_transitions(id, action_id, checkpoint_id, status, created_at)
SELECT 'action-open-' || id, 'action-' || id, id, 'open', created_at
FROM checkpoints
WHERE status <> 'completed';

INSERT INTO checkpoint_action_transitions(id, action_id, checkpoint_id, status, created_at)
SELECT 'action-carried-' || older.id, 'action-' || older.id, newer.id, 'carried_forward', newer.created_at
FROM checkpoints older
JOIN checkpoints newer ON newer.id = (
    SELECT candidate.id FROM checkpoints candidate
    WHERE candidate.project_id = older.project_id
      AND (candidate.created_at > older.created_at OR (candidate.created_at = older.created_at AND candidate.id > older.id))
    ORDER BY candidate.created_at, candidate.id LIMIT 1
)
WHERE older.status <> 'completed';
