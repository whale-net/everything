-- Reverse migration 006. Does not restore 005's table -- migration history
-- is append-only, so a down-migration to before 006 leaves mcp_credential
-- absent (matching 005's own down-migration behavior) rather than
-- recreating the pre-mcpauth shape.

DROP TABLE IF EXISTS mcp_credential;
