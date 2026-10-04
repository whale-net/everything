-- 039_mcp_credential_name: an optional operator-chosen label on an MCP
-- credential, so the credentials page can list a Name instead of an opaque
-- id. krill owns this table (see 006's "Schema ownership" comment), and
-- this is an expand-only step.
--
-- Nullable on purpose: every credential minted before this migration (by
-- selfserve.go's JSON POST /credentials and token.go's authorization-code
-- path, neither of which collects a name) keeps working untouched, and no
-- consumer is forced to backfill. NULL names are excluded from the index
-- below, so unnamed rows never collide with each other.
ALTER TABLE mcp_credential ADD COLUMN name TEXT;

-- One LIVE credential per (identity, name). The partial predicate is what
-- makes revoking free a name: a revoked row leaves the index, so an
-- operator can reuse the name on the next credential. Live rows only --
-- two revoked rows sharing a name are harmless history.
CREATE UNIQUE INDEX mcp_credential_identity_name_live
    ON mcp_credential(identity, name)
    WHERE revoked_at IS NULL AND name IS NOT NULL;