-- Derived inventory only: it is not a receipt that a record was applied.
-- Rebuilding or removing it never changes canonical observations.
CREATE TABLE history_index_blocks (
    project_id TEXT NOT NULL REFERENCES projects(id),
    block_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    PRIMARY KEY(project_id, block_hash)
) STRICT;
CREATE TABLE history_index_records (
    project_id TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    envelope_hash TEXT NOT NULL,
    record_type TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_revision INTEGER NOT NULL,
    envelope_json TEXT NOT NULL,
    PRIMARY KEY(project_id, block_hash, envelope_hash),
    FOREIGN KEY(project_id, block_hash) REFERENCES history_index_blocks(project_id, block_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_identity ON history_index_records(project_id, record_type, record_id, record_revision);
CREATE TABLE history_index_refs (
    project_id TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    envelope_hash TEXT NOT NULL,
    ref_kind TEXT NOT NULL,
    ref_id TEXT NOT NULL,
    PRIMARY KEY(project_id, block_hash, envelope_hash, ref_kind, ref_id),
    FOREIGN KEY(project_id, block_hash, envelope_hash) REFERENCES history_index_records(project_id, block_hash, envelope_hash) ON DELETE CASCADE
) STRICT;
CREATE INDEX history_index_dependents ON history_index_refs(project_id, ref_kind, ref_id);

-- Proof that the exact set passed semantic validation at this index version.
-- It is not an import receipt and must be rechecked against local records.
CREATE TABLE history_index_valid_sets (
    project_id TEXT NOT NULL REFERENCES projects(id),
    set_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL,
    local_state_hash TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(project_id, set_hash)
) STRICT;

-- One bounded reference-free narrative set, used to validate additive
-- imports without reprocessing the earlier observations.
CREATE TABLE history_index_narrative_tip (
    project_id TEXT PRIMARY KEY REFERENCES projects(id),
    set_hash TEXT NOT NULL,
    index_version INTEGER NOT NULL
) STRICT;
CREATE TABLE history_index_narrative_members (
    project_id TEXT NOT NULL REFERENCES history_index_narrative_tip(project_id) ON DELETE CASCADE,
    envelope_hash TEXT NOT NULL,
    PRIMARY KEY(project_id, envelope_hash)
) STRICT;

-- Local publication intent. Each block is staged in the validated inventory
-- first; a later sync republishes it if endpoint delivery was interrupted.
CREATE TABLE history_index_publications (
    project_id TEXT NOT NULL,
    endpoint_hash TEXT NOT NULL,
    block_hash TEXT NOT NULL,
    PRIMARY KEY(project_id, endpoint_hash, block_hash),
    FOREIGN KEY(project_id, block_hash) REFERENCES history_index_blocks(project_id, block_hash) ON DELETE CASCADE
) STRICT;
