-- Corrections to transport interpretation, never to the original observations.
CREATE TABLE redaction_recoveries (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL,
    original_sha256 TEXT NOT NULL,
    derived_sha256 TEXT NOT NULL,
    original_content_sha256 TEXT NOT NULL,
    derived_content_sha256 TEXT NOT NULL,
    envelope_json TEXT NOT NULL,
    PRIMARY KEY(project_id,id),
    UNIQUE(project_id,derived_sha256)
) STRICT;
CREATE TRIGGER redaction_recoveries_no_update BEFORE UPDATE ON redaction_recoveries BEGIN SELECT RAISE(ABORT,'redaction recoveries are append-only'); END;
CREATE TRIGGER redaction_recoveries_no_delete BEFORE DELETE ON redaction_recoveries BEGIN SELECT RAISE(ABORT,'redaction recoveries are append-only'); END;
