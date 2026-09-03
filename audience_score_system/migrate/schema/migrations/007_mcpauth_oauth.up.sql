-- mcp_oauth_client and mcp_auth_code, owned and versioned by ASS per NFR5
-- -- mcpauth ships no migrations of its own (see
-- libs/go/mcpauth/README.md "Schema contract" for both tables' exact
-- contract). Issue #1646 wires mcpauth's OAuth2 authorization-code + PKCE
-- front end (Provider: /authorize, /token, /register, discovery metadata)
-- onto `web`, which needs a ClientRegistry and an AuthCodeStore backed by
-- these two tables -- ASS MUST use mcpauth.NewPostgresClientRegistry and
-- mcpauth.NewPostgresAuthCodeStore (not the in-memory defaults), since
-- `/authorize`, `/token`, and `/register` can all land on different `web`
-- replicas.
--
-- Both tables use mcpauth's fully generic contract verbatim -- unlike
-- migration 006's mcp_credential, neither needs an ASS-specific foreign
-- key: mcp_oauth_client has no identity column at all (a registered OAuth2
-- client isn't owned by any one Person), and mcp_auth_code's `identity`
-- column holds the Person UUID as an opaque string (the same value
-- web/auth.Authenticator.MCPCallerResolver reads off the session and the
-- same value that ends up in mcp_credential.person_id) rather than a typed
-- FK, exactly as mcpauth.AuthCodeStore treats it.
--
-- Migration history is append-only -- do not edit 005 or 006.

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
