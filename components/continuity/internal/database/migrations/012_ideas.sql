CREATE TABLE ideas (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    task_id TEXT,
    checkpoint_id TEXT,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, id),
    FOREIGN KEY(project_id) REFERENCES projects(id)
) STRICT;

CREATE TABLE idea_tags (
    project_id TEXT NOT NULL,
    idea_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY(project_id, idea_id, tag),
    FOREIGN KEY(project_id, idea_id) REFERENCES ideas(project_id, id)
) STRICT;

CREATE TABLE idea_dispositions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    idea_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('integrated', 'discarded', 'deferred', 'merged')),
    reason TEXT NOT NULL,
    target_refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority = 'caller_asserted'),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    UNIQUE(project_id, idea_id),
    FOREIGN KEY(project_id, idea_id) REFERENCES ideas(project_id, id)
) STRICT;

CREATE INDEX ideas_project_time ON ideas(project_id, recorded_at, id);
CREATE INDEX idea_tags_project_tag ON idea_tags(project_id, tag, idea_id);
