-- Reverse of 039_mcp_credential_name: drop the index before the column it
-- is built on.
DROP INDEX IF EXISTS mcp_credential_identity_name_live;
ALTER TABLE mcp_credential DROP COLUMN IF EXISTS name;