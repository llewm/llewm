-- Usage accuracy rebuild. Codex subagent rollouts are now marked as sidechain
-- requests and Codex tool failures are recognized from structured output. The
-- raw usage tables are rebuildable from local session files, so clear their
-- rows and cursors to correct existing observations on the next ingestion.
--
-- Usage-summary schema 2 carries the corrected sidechain/overhead semantics.
-- Keep schema 1 imports readable while allowing a corrected schema 2 summary
-- with the same session end and request count to supersede it.
ALTER TABLE usage_session_summaries RENAME TO usage_session_summaries_v1;
DROP INDEX usage_session_summaries_session;

CREATE TABLE usage_session_summaries (
    project_id TEXT NOT NULL REFERENCES projects(id),
    id TEXT NOT NULL,
    client TEXT NOT NULL,
    session_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    task_id TEXT NOT NULL,
    overhead_tokens INTEGER NOT NULL CHECK (overhead_tokens >= 0),
    models_json TEXT NOT NULL,
    tools_json TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version IN (1, 2)),
    PRIMARY KEY (project_id, id)
) STRICT;

INSERT INTO usage_session_summaries
SELECT * FROM usage_session_summaries_v1;
DROP TABLE usage_session_summaries_v1;
CREATE INDEX usage_session_summaries_session ON usage_session_summaries(project_id, client, session_id, last_at);

DELETE FROM usage_tool_results;
DELETE FROM usage_tool_calls;
DELETE FROM usage_requests;
DELETE FROM usage_sources;
