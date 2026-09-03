-- mcp_credential, recreated against libs/go/mcpauth's schema contract
-- (see libs/go/mcpauth/README.md "Schema contract") -- issue #1643 migrates
-- `mcp`'s caller-auth store off the bespoke store.CredentialStore (migration
-- 005) and onto the shared mcpauth.CredentialStore. Migration history is
-- append-only (libs/go/migrate tracks applied history), so this drops and
-- recreates the table rather than editing 005 in place.
--
-- The shape is mcpauth's generic contract with ASS's own referential
-- integrity layered on top (FR13, NFR5): person_id is a real foreign key to
-- person(id), not a generic string identity column, and both indexes 005
-- established are preserved verbatim. mcpauth itself treats identity as an
-- opaque string (StoreConfig.IdentityColumn = "person_id",
-- StoreConfig.IdentityCast = "uuid" tell it how to bind/cast against this
-- column) -- that genericity must not become a reason ASS loses its FK.
--
-- Deliberately not SCD2 -- same as 005, a credential's lifecycle is
-- mint-then-revoke (a one-way revoked_at close), not a dimension whose
-- value changes over time and needs history (see
-- libs/go/mcpauth/README.md "Not SCD2").

DROP TABLE IF EXISTS mcp_credential;

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
