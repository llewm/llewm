-- Usage ingestion (8a of continuity-auditing, decision usage-tracking-design).
-- Exact provider-reported usage is read from client session files (Claude Code
-- transcripts, Codex rollouts): one row per model request, one per tool call,
-- and one per tool result with its size. Only usage fields, tool names, and
-- sizes are stored, never content. All four tables are device-local; the
-- never-populated usage_records table is replaced.
DROP TABLE usage_records;

CREATE TABLE usage_requests (
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL CHECK (client IN ('claude-code', 'codex')),
    session_id TEXT NOT NULL,
    request_id TEXT NOT NULL CHECK (request_id <> ''),
    model TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    -- input_tokens excludes cached tokens for every client.
    input_tokens INTEGER NOT NULL CHECK (input_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL CHECK (cache_write_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL CHECK (cache_read_tokens >= 0),
    -- output_tokens includes reasoning tokens, which are also given alone.
    output_tokens INTEGER NOT NULL CHECK (output_tokens >= 0),
    reasoning_tokens INTEGER NOT NULL CHECK (reasoning_tokens >= 0),
    sidechain INTEGER NOT NULL CHECK (sidechain IN (0, 1)),
    PRIMARY KEY (project_id, client, request_id)
) STRICT;
CREATE INDEX usage_requests_session ON usage_requests(project_id, session_id, occurred_at);
CREATE INDEX usage_requests_time ON usage_requests(project_id, occurred_at);

CREATE TABLE usage_tool_calls (
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    session_id TEXT NOT NULL,
    call_id TEXT NOT NULL CHECK (call_id <> ''),
    tool TEXT NOT NULL CHECK (tool <> ''),
    occurred_at TEXT NOT NULL,
    PRIMARY KEY (project_id, client, call_id)
) STRICT;
CREATE INDEX usage_tool_calls_session ON usage_tool_calls(project_id, session_id);

CREATE TABLE usage_tool_results (
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    session_id TEXT NOT NULL,
    call_id TEXT NOT NULL CHECK (call_id <> ''),
    result_bytes INTEGER NOT NULL CHECK (result_bytes >= 0),
    is_error INTEGER NOT NULL CHECK (is_error IN (0, 1)),
    occurred_at TEXT NOT NULL,
    PRIMARY KEY (project_id, client, call_id)
) STRICT;

-- Where ingestion stopped in each session file: the byte offset after the
-- last complete line, and whether the file belongs to this project.
CREATE TABLE usage_sources (
    project_id TEXT NOT NULL REFERENCES projects(id),
    client TEXT NOT NULL,
    path TEXT NOT NULL,
    session_id TEXT NOT NULL,
    matched INTEGER NOT NULL CHECK (matched IN (0, 1)),
    size INTEGER NOT NULL,
    offset INTEGER NOT NULL,
    scanned_at TEXT NOT NULL,
    PRIMARY KEY (project_id, path)
) STRICT;
