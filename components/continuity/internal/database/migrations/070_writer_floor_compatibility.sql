-- MCP API revisions 103 through 105 did not introduce write-incompatible
-- semantics, but ordinary opens raised the floor from 102 anyway. Restore the
-- last actual writer compatibility boundary. Keep the legacy mcp_api column so
-- already-running API 102+ servers can observe the correction and checkpoint.
UPDATE writer_floor SET mcp_api = 102, raised_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE mcp_api > 102;
