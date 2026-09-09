-- mcp_credential, mcp_oauth_client, and mcp_auth_code -- the schema
-- libs/go/mcpauth needs, owned and versioned by whagent-net per that
-- library's "Schema ownership" contract (libs/go/mcpauth's package doc and
-- README.md "Schema contract" -- mcpauth ships no migration of its own,
-- NFR5 there). Issue #2245 (FR9/C27, NFR7) wires mcpauth's OAuth2
-- authorization-code + PKCE front end (Provider: /authorize, /token,
-- /register, discovery metadata) onto `ui`, the binary that holds the
-- operator's Keycloak browser session (whagent_net/ui/mcpauth.go).
--
-- mcp_credential uses mcpauth's fully generic contract verbatim
-- (StoreConfig.IdentityColumn defaults to "identity", a plain TEXT
-- column) -- NOT a typed foreign key, unlike audience_score_system's
-- person_id-keyed variant. NFR7 is the reason: the identity this column
-- holds is the operator's LB2 (iss, sub) pair (whagent_net/session.Subject
-- shape, the same pair sessions.subject_iss/subject_sub already store),
-- encoded as a single opaque string -- there is no whagent-net user/person
-- table for a foreign key to point at, and this migration must not
-- introduce one.
--
-- mcp_oauth_client and mcp_auth_code are mcpauth's fully generic contract
-- too: `ui` MUST construct mcpauth.NewPostgresClientRegistry and
-- mcpauth.NewPostgresAuthCodeStore (not the in-memory defaults), since
-- `/authorize`, `/token`, and `/register` can all land on different `ui`
-- replicas. mcp_auth_code's `identity` column holds the same encoded
-- (iss, sub) string as mcp_credential.identity, per mcpauth.AuthCodeStore.
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

-- Supports "list this operator's credentials" (a future revoke-from-ui
-- affordance -- explicitly out of scope for M2, README.md "no revocation
-- UI") without a table scan.
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
