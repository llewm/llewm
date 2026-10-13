-- Parallel-agent safety. Device-local tables; none are synced.
--
-- writer_floor holds the highest MCP API any process has opened this store
-- with. A long-running process (an MCP server started before an upgrade) whose
-- API is lower refuses writes, so superseded semantics never write again.
CREATE TABLE writer_floor (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    mcp_api INTEGER NOT NULL CHECK (mcp_api > 0),
    raised_at TEXT NOT NULL
) STRICT;

-- head_observations is the last HEAD a hook saw for each repository member, so
-- the next hook notices when HEAD moved during a session's tool call.
CREATE TABLE head_observations (
    project_id TEXT NOT NULL REFERENCES projects(id),
    member TEXT NOT NULL,
    head TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, member)
) STRICT;

-- commit_authors records which session a hook saw create a commit, and that
-- session's task when known. Machine-observed attribution, not authorization.
CREATE TABLE commit_authors (
    project_id TEXT NOT NULL REFERENCES projects(id),
    member TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    session_id TEXT NOT NULL,
    client TEXT NOT NULL,
    task_id TEXT,
    tool_name TEXT,
    certainty TEXT NOT NULL CHECK (certainty IN ('observed', 'ambiguous')),
    observed_at TEXT NOT NULL,
    PRIMARY KEY (project_id, member, commit_sha)
) STRICT;
CREATE INDEX commit_authors_session ON commit_authors(project_id, session_id);
CREATE INDEX commit_authors_task ON commit_authors(project_id, task_id);
