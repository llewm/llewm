-- Complete reference baselines validate recovery lookup metadata. Guarded
-- comparisons probe only selected identities; all other callers retain the
-- complete recovery reader. This index changes no canonical writer semantics.
CREATE INDEX redaction_recoveries_identity ON redaction_recoveries(project_id,record_type,record_id,record_revision);
