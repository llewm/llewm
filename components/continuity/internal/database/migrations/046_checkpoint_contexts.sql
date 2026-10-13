-- What a checkpoint was written on, observed by the machine that wrote it: the
-- checkpoint of the same task it continued (the explicit predecessor, else the
-- task's tip at that moment) and each repository's HEAD commit. Sync carries it,
-- so another machine can tell when memory is ahead of its code and, later, when
-- two machines continued the same checkpoint. One row per checkpoint, append-only.
CREATE TABLE checkpoint_contexts (
    project_id TEXT NOT NULL REFERENCES projects(id),
    checkpoint_id TEXT NOT NULL,
    based_on_checkpoint_id TEXT,
    code_heads_json TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, checkpoint_id),
    FOREIGN KEY (project_id, checkpoint_id) REFERENCES checkpoints(project_id, id)
) STRICT;

CREATE INDEX checkpoint_contexts_based_on ON checkpoint_contexts(project_id, based_on_checkpoint_id);

CREATE TRIGGER checkpoint_contexts_no_update BEFORE UPDATE ON checkpoint_contexts BEGIN SELECT RAISE(ABORT,'checkpoint contexts are append-only'); END;
CREATE TRIGGER checkpoint_contexts_no_delete BEFORE DELETE ON checkpoint_contexts BEGIN SELECT RAISE(ABORT,'checkpoint contexts are append-only'); END;
