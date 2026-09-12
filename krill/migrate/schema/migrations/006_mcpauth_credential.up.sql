-- mcp_credential, mcp_oauth_client, and mcp_auth_code -- the schema
-- //libs/go/mcpauth needs, owned and versioned by krill per that
-- library's "Schema ownership" contract (mcpauth ships no migration of
-- its own). Backs the mcpauth (human) front door on krill/mcp
-- (krill/mcp/server/auth.go, NFR1) and the OAuth2 authorization-server
-- endpoints krill/ui mounts (krill/ui/mcpauth.go) -- see ARCHITECTURE.md
-- "The MCP spec surface" and "krill/ui and the mcpauth front door".
--
-- mcp_credential uses mcpauth's fully generic contract verbatim
-- (StoreConfig.IdentityColumn defaults to "identity", a plain TEXT
-- column) -- not a typed foreign key: krill has no person/user table
-- (NFR1 authorizes by persona, never by individual identity), so the
-- identity this column holds is whatever opaque string krill/identity
-- encodes the signed-in operator's (iss, sub) pair into.
--
-- mcp_oauth_client and mcp_auth_code are mcpauth's fully generic contract
-- too: krill/ui MUST construct mcpauth.NewPostgresClientRegistry and
-- mcpauth.NewPostgresAuthCodeStore (not the in-memory defaults), since
-- `/authorize`, `/token`, and `/register` can all land on different
-- krill/ui replicas. mcp_auth_code's `identity` column holds the same
-- encoded (iss, sub) string as mcp_credential.identity.
--
-- Not SCD2 -- see libs/go/mcpauth/README.md "Not SCD2": a credential's
-- lifecycle is mint-then-revoke (a one-way revoked_at close), not a
-- dimension whose value changes over time and needs history.

CREATE TABLE mcp_credential (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    identity     TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

-- Supports "resolve this bearer token's hash to a live credential" (every
-- mcp auth middleware call) without a table scan. token_hash is already
-- UNIQUE, so this mirrors that constraint as an explicit lookup index.
CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;

-- Supports "list this operator's credentials" without a table scan.
CREATE INDEX mcp_credential_identity ON mcp_credential(identity);

CREATE TABLE mcp_oauth_client (
    client_id  TEXT        PRIMARY KEY,
    metadata   JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE mcp_auth_code (
    code_hash             TEXT        PRIMARY KEY,
    client_id             TEXT        NOT NULL,
    redirect_uri          TEXT        NOT NULL,
    identity              TEXT        NOT NULL,
    code_challenge        TEXT        NOT NULL,
    code_challenge_method TEXT        NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Supports pruning/rejecting expired pending authorization codes (POST
-- /token's expiry check, and any future sweep) without a table scan.
CREATE INDEX mcp_auth_code_expires_at ON mcp_auth_code(expires_at);
