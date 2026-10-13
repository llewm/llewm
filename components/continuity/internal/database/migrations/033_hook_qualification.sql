CREATE TABLE hook_qualification_runs (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_key TEXT NOT NULL,
 request_sha256 TEXT NOT NULL,
 supersedes_id TEXT,
 client TEXT NOT NULL,
 client_version TEXT NOT NULL,
 execution_mode TEXT NOT NULL,
 hook_sha256 TEXT NOT NULL,
 matrix_version INTEGER NOT NULL CHECK(matrix_version = 1),
 recorded_at TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 UNIQUE(id, project_id),
 UNIQUE(project_id, request_key),
 FOREIGN KEY(supersedes_id, project_id) REFERENCES hook_qualification_runs(id, project_id)
) STRICT;
CREATE INDEX hook_qualification_project_client_time ON hook_qualification_runs(project_id, client, recorded_at DESC);
CREATE TRIGGER hook_qualification_no_update BEFORE UPDATE ON hook_qualification_runs
BEGIN SELECT RAISE(ABORT,'hook qualification history is append-only'); END;
CREATE TRIGGER hook_qualification_no_delete BEFORE DELETE ON hook_qualification_runs
BEGIN SELECT RAISE(ABORT,'hook qualification history is append-only'); END;
