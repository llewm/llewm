-- Usage statistics (8b of continuity-auditing). Cache writes are priced by
-- their lifetime (Anthropic: 1.25x input for 5 minutes, 2x for 1 hour), so
-- requests record how many cache-write tokens used the long lifetime. The
-- usage tables are device-local and rebuilt from the session files, so the
-- rows and cursors are cleared and the next ingestion reads everything again.
DELETE FROM usage_tool_results;
DELETE FROM usage_tool_calls;
DELETE FROM usage_requests;
DELETE FROM usage_sources;
ALTER TABLE usage_requests ADD COLUMN cache_write_long_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_long_tokens >= 0);
