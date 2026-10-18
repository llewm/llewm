-- Device-local import receipts. Curated nodes/plans and their source hashes
-- travel through ordinary portable records; these replay keys do not.
CREATE TABLE prose_imports (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    bundle_sha256 TEXT NOT NULL,
    receipt_json TEXT NOT NULL,
    PRIMARY KEY(project_id,id)
) STRICT;
CREATE TRIGGER prose_imports_no_update BEFORE UPDATE ON prose_imports BEGIN SELECT RAISE(ABORT,'prose imports are append-only'); END;
CREATE TRIGGER prose_imports_no_delete BEFORE DELETE ON prose_imports BEGIN SELECT RAISE(ABORT,'prose imports are append-only'); END;
