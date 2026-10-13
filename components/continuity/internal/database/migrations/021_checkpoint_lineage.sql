ALTER TABLE checkpoints ADD COLUMN predecessor_checkpoint_id TEXT REFERENCES checkpoints(id);

CREATE UNIQUE INDEX checkpoints_one_continuation_per_predecessor
    ON checkpoints(project_id, predecessor_checkpoint_id)
    WHERE predecessor_checkpoint_id IS NOT NULL;

CREATE INDEX checkpoints_project_predecessor
    ON checkpoints(project_id, predecessor_checkpoint_id);
