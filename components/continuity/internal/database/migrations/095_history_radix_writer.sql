-- Git format5 changes selected block paths and requires explicit conversion.
-- Stop all older writers before activating the new transport; SQL migration
-- alone does not convert any clone or publish new wire history.
INSERT INTO writer_floor(singleton,mcp_api,raised_at)
VALUES(1,138,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT(singleton) DO UPDATE SET mcp_api=excluded.mcp_api,
raised_at=excluded.raised_at WHERE excluded.mcp_api>writer_floor.mcp_api;
