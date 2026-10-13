CREATE TABLE evidence_records (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    record_type TEXT NOT NULL CHECK (record_type = 'commit_narrative'),
    commit_id TEXT NOT NULL,
    task_id TEXT,
    title TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    content_authority TEXT NOT NULL CHECK (content_authority = 'caller_asserted'),
    commit_authority TEXT NOT NULL CHECK (commit_authority = 'machine_observed'),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('manual', 'git_note')),
    source_ref TEXT,
    content_sha256 TEXT NOT NULL,
    supersedes_id TEXT,
    recorded_at TEXT NOT NULL,
    commit_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    payload_json TEXT NOT NULL,
    UNIQUE(project_id, id),
    FOREIGN KEY(project_id) REFERENCES projects(id),
    FOREIGN KEY(supersedes_id) REFERENCES evidence_records(id)
) STRICT;

CREATE TABLE evidence_record_paths (
    project_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    path TEXT NOT NULL,
    PRIMARY KEY(project_id, record_id, path),
    FOREIGN KEY(project_id, record_id)
        REFERENCES evidence_records(project_id, id)
) STRICT;

CREATE INDEX evidence_records_project_time
    ON evidence_records(project_id, recorded_at DESC, id DESC);
CREATE INDEX evidence_records_project_commit
    ON evidence_records(project_id, commit_id, recorded_at DESC, id DESC);
CREATE INDEX evidence_records_project_task
    ON evidence_records(project_id, task_id, recorded_at DESC, id DESC);
CREATE INDEX evidence_records_project_type
    ON evidence_records(project_id, record_type, recorded_at DESC, id DESC);
CREATE INDEX evidence_record_paths_project_path
    ON evidence_record_paths(project_id, path, record_id);
CREATE UNIQUE INDEX evidence_records_one_successor
    ON evidence_records(supersedes_id)
    WHERE supersedes_id IS NOT NULL;
