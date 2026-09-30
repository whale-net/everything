-- Persona resolved from the caller's realm roles at mint time. Nullable:
-- rows minted before this migration carry no persona, and nothing enforces
-- on the column yet (expand step).
ALTER TABLE mcp_credential ADD COLUMN persona TEXT;
