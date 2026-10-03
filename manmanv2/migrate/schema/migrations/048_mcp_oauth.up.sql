-- OAuth authorization server tables for the MCP (libs/go/auth) and the
-- stored per-user Keycloak grants the MCP acts with (libs/go/grpcauth).
-- Same shape as krill/ASS/whagent-net. Current-state/mint-then-revoke
-- tables, not SCD2.

CREATE TABLE mcp_credential (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    identity     TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;
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
CREATE INDEX mcp_auth_code_expires_at ON mcp_auth_code(expires_at);

CREATE TABLE grpcauth_delegated_grant (
    subject        TEXT        NOT NULL,
    grant_key      TEXT        NOT NULL,
    token_material BYTEA       NOT NULL,
    status         TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject, grant_key)
);
