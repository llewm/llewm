-- Resources are named things a project works with (hosts, repositories,
-- services). Revisions describe a resource; notes are dated observations of its
-- state that do not need a new revision. Neither ever holds a credential.

CREATE TABLE resources (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    kind TEXT NOT NULL CHECK (kind IN ('host', 'repository', 'service')),
    title TEXT NOT NULL,
    summary TEXT NOT NULL,
    attributes_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('proposed', 'accepted', 'retired')),
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id, revision)
) STRICT;

CREATE INDEX resources_project_kind ON resources(project_id, kind, id, revision DESC);

CREATE TABLE resource_notes (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    resource_id TEXT NOT NULL,
    text TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('machine_observed', 'user_reported', 'caller_asserted')),
    creator TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX resource_notes_resource ON resource_notes(project_id, resource_id, observed_at DESC, id);

CREATE TRIGGER resources_index AFTER INSERT ON resources
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT NEW.title,
        NEW.summary || COALESCE(char(10) || (SELECT group_concat(a.key || ': ' || a.value, char(10)) FROM json_each(NEW.attributes_json) a), ''),
        NEW.project_id, 'resource', 'resource:' || NEW.id || '@' || NEW.revision, NEW.kind, NEW.recorded_at;
END;

CREATE TRIGGER resource_notes_index AFTER INSERT ON resource_notes
BEGIN
    INSERT INTO history_chunks(title, body, project_id, family, ref, kind, recorded_at)
    SELECT COALESCE((SELECT r.title FROM resources r WHERE r.project_id = NEW.project_id AND r.id = NEW.resource_id ORDER BY r.revision DESC LIMIT 1), NEW.resource_id),
        NEW.text, NEW.project_id, 'resource', 'resource:' || NEW.resource_id || '#' || NEW.id, 'note', NEW.recorded_at;
END;
