ALTER TABLE checkpoints ADD COLUMN task_id TEXT;

CREATE INDEX checkpoints_project_task_time
    ON checkpoints(project_id, task_id, created_at DESC, id DESC);

CREATE INDEX checkpoints_project_session_time
    ON checkpoints(project_id, session_id, created_at DESC, id DESC);
