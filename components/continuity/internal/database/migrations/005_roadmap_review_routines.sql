CREATE TABLE roadmap_review_routines (
    project_id TEXT NOT NULL,
    routine_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'superseded')),
    commit_threshold INTEGER CHECK (commit_threshold IS NULL OR commit_threshold > 0),
    elapsed_seconds_threshold INTEGER CHECK (elapsed_seconds_threshold IS NULL OR elapsed_seconds_threshold > 0),
    changed_files_threshold INTEGER CHECK (changed_files_threshold IS NULL OR changed_files_threshold > 0),
    source TEXT NOT NULL,
    source_ref TEXT,
    authority TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, routine_id, revision),
    FOREIGN KEY(project_id) REFERENCES projects(id)
) STRICT;

CREATE INDEX roadmap_review_routines_project_status
    ON roadmap_review_routines(project_id, status, recorded_at DESC);
