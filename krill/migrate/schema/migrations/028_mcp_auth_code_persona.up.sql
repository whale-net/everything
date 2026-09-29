-- Persona vetted at /authorize, carried to /token so the minted
-- mcp_credential.persona records it. Nullable: pending codes never outlive
-- the auth-code TTL, so no backfill is needed.
ALTER TABLE mcp_auth_code ADD COLUMN persona TEXT;
