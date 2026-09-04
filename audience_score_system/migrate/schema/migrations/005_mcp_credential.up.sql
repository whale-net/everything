-- mcp_credential: the caller-auth mechanism `mcp` uses to resolve a bearer
-- credential to a Person (issue #1575's Scaffold design decision -- see
-- ../../../ARCHITECTURE.md "MCP server: caller authentication").
--
-- The credential itself is a high-entropy (crypto/rand-generated) random
-- token, shown to the Person exactly once at mint time. Only its SHA-256
-- hash is ever persisted here -- token_hash is not reversible, so a
-- database read (or leak) cannot recover a usable credential, mirroring
-- how web_session never stores a raw refresh token (see
-- web/auth/session.go's encryptToken) except this is one-way hashing, not
-- encryption, since the server never needs the raw value back.
--
-- Deliberately not SCD2 -- a credential's lifecycle is mint-then-revoke,
-- the same simple lifecycle shape as channel_invite's invalidated_at, not
-- a dimension whose value changes over time.

CREATE TABLE mcp_credential (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    person_id    UUID        NOT NULL REFERENCES person(id),
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

-- Supports "resolve this bearer token's hash to a live credential" (every
-- mcp auth middleware call) without a table scan. token_hash is already
-- UNIQUE, so this mirrors that constraint as an explicit lookup index.
CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;

-- Supports "list this Person's credentials" (a future revoke-from-web-UI
-- affordance) without a table scan.
CREATE INDEX mcp_credential_person_id ON mcp_credential(person_id);
