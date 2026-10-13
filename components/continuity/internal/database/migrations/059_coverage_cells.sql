-- Coverage (7a of continuity-multi-repo-sync) replaces client capability
-- evidence and hook qualification runs, two record families for one claim:
-- "client X on platform Y does Z, per source S" (docs/consolidation-review.md).
-- Requirements are authored in repository Markdown; a cell references one by
-- ID and records status, verification level, how it was implemented, and
-- links. Cells are device-local and append-only; the latest per requirement,
-- client, and platform is current. The two old tables held 2 rows and 0 rows
-- and were never portable; the pre-migration backup keeps them.
DROP TABLE hook_qualification_runs;
DROP TABLE client_capability_evidence;

CREATE TABLE coverage_cells (
    id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id),
    requirement_id TEXT NOT NULL CHECK (requirement_id <> ''),
    client TEXT NOT NULL CHECK (client <> ''),
    platform TEXT NOT NULL CHECK (platform <> ''),
    client_version TEXT,
    status TEXT NOT NULL CHECK (status IN ('unsupported', 'partial', 'supported', 'failing')),
    verification TEXT NOT NULL CHECK (verification IN ('inferred', 'reported', 'observed', 'tested')),
    implementation TEXT,
    refs_json TEXT NOT NULL,
    creator TEXT NOT NULL,
    model TEXT,
    authority TEXT NOT NULL CHECK (authority IN ('caller_asserted', 'user_reported', 'model_suggested')),
    recorded_at TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    PRIMARY KEY (project_id, id)
) STRICT;

CREATE INDEX coverage_cells_current ON coverage_cells(project_id, requirement_id, client, platform, recorded_at DESC, id DESC);
CREATE INDEX coverage_cells_client ON coverage_cells(project_id, client, recorded_at DESC);

CREATE TRIGGER coverage_cells_no_update BEFORE UPDATE ON coverage_cells BEGIN SELECT RAISE(ABORT,'coverage cells are append-only'); END;
CREATE TRIGGER coverage_cells_no_delete BEFORE DELETE ON coverage_cells BEGIN SELECT RAISE(ABORT,'coverage cells are append-only'); END;
