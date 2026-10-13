CREATE TABLE idea_dispositions_next (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    idea_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('accepted', 'integrated', 'discarded', 'deferred', 'merged')),
    reason TEXT NOT NULL,
    target_refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    FOREIGN KEY(project_id, idea_id) REFERENCES ideas(project_id, id)
) STRICT;

INSERT INTO idea_dispositions_next(
    id, project_id, idea_id, status, reason, target_refs_json, creator, model,
    authority, recorded_at, schema_version
)
SELECT
    id, project_id, idea_id, status, reason, target_refs_json, creator, model,
    authority, recorded_at, schema_version
FROM idea_dispositions;

DROP TABLE idea_dispositions;
ALTER TABLE idea_dispositions_next RENAME TO idea_dispositions;

CREATE INDEX idea_dispositions_project_idea_time
    ON idea_dispositions(project_id, idea_id, recorded_at, id);
CREATE UNIQUE INDEX idea_dispositions_one_acceptance
    ON idea_dispositions(project_id, idea_id) WHERE status = 'accepted';
CREATE UNIQUE INDEX idea_dispositions_one_terminal
    ON idea_dispositions(project_id, idea_id) WHERE status != 'accepted';
