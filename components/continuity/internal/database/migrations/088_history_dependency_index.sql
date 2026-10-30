-- Disposable, versioned dependency selectors derived from validated envelopes.
-- No target foreign key: unresolved targets must survive until their arrival.
-- The digest binds the complete selector set, support flag, envelope hash and
-- interpretation version. Readers repair missing or damaged sets from bytes.
CREATE TABLE history_index_dependency_records (
    project_id TEXT NOT NULL REFERENCES projects(id),
    envelope_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    supported INTEGER NOT NULL CHECK(supported IN (0,1)),
    selectors_sha256 TEXT NOT NULL,
    PRIMARY KEY(project_id,envelope_hash)
) STRICT;
CREATE TABLE history_index_dependencies (
    project_id TEXT NOT NULL,
    envelope_hash TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    selection_mode TEXT NOT NULL CHECK(selection_mode IN ('exact','any','prior')),
    target_revision INTEGER NOT NULL CHECK(target_revision>=0),
    PRIMARY KEY(project_id,envelope_hash,target_type,target_id,selection_mode,target_revision),
    FOREIGN KEY(project_id,envelope_hash) REFERENCES history_index_dependency_records(project_id,envelope_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_dependency_targets ON history_index_dependencies(project_id,target_type,target_id,selection_mode,target_revision);
