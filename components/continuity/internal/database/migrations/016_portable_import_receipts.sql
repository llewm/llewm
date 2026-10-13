CREATE TABLE portable_import_receipts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    lineage_id TEXT NOT NULL,
    export_id TEXT NOT NULL,
    source_instance_id TEXT NOT NULL,
    manifest_sha256 TEXT NOT NULL,
    records_sha256 TEXT NOT NULL,
    record_set_sha256 TEXT NOT NULL,
    selection_profile TEXT NOT NULL,
    redaction_profile_id TEXT NOT NULL,
    redaction_profile_version INTEGER NOT NULL,
    redaction_profile_sha256 TEXT NOT NULL,
    appended_records INTEGER NOT NULL CHECK (appended_records >= 0),
    already_present_records INTEGER NOT NULL CHECK (already_present_records >= 0),
    applied_at TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority = 'machine_observed'),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

CREATE INDEX portable_import_receipts_project_time
    ON portable_import_receipts(project_id, applied_at DESC, id DESC);
