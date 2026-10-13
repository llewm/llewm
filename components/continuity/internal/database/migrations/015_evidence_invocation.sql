ALTER TABLE evidence_records ADD COLUMN creator_invocation_id TEXT REFERENCES actor_invocations(id);

CREATE INDEX evidence_records_creator_invocation
    ON evidence_records(project_id, creator_invocation_id)
    WHERE creator_invocation_id IS NOT NULL;

CREATE TRIGGER evidence_records_creator_invocation_project_insert
BEFORE INSERT ON evidence_records
WHEN NEW.creator_invocation_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM actor_invocations
    WHERE id = NEW.creator_invocation_id AND project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'creator invocation must belong to evidence project');
END;

CREATE TRIGGER evidence_records_creator_invocation_project_update
BEFORE UPDATE OF creator_invocation_id, project_id ON evidence_records
WHEN NEW.creator_invocation_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM actor_invocations
    WHERE id = NEW.creator_invocation_id AND project_id = NEW.project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'creator invocation must belong to evidence project');
END;
