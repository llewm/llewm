-- Which branch each repository was on when a checkpoint was written, observed by
-- the machine that wrote it (an empty branch means a detached HEAD). It sits
-- beside the checkpoint's context rather than inside it, and travels as its own
-- record type, so builds that predate it defer it instead of refusing the
-- context. Session selection uses it as a hint to resume the task of the current
-- branch; no record is keyed by a branch. One row per checkpoint, append-only.
CREATE TABLE checkpoint_branches (
    project_id TEXT NOT NULL REFERENCES projects(id),
    checkpoint_id TEXT NOT NULL,
    branches_json TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    observed_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, checkpoint_id),
    FOREIGN KEY (project_id, checkpoint_id) REFERENCES checkpoints(project_id, id)
) STRICT;

CREATE TRIGGER checkpoint_branches_no_update BEFORE UPDATE ON checkpoint_branches BEGIN SELECT RAISE(ABORT,'checkpoint branches are append-only'); END;
CREATE TRIGGER checkpoint_branches_no_delete BEFORE DELETE ON checkpoint_branches BEGIN SELECT RAISE(ABORT,'checkpoint branches are append-only'); END;
