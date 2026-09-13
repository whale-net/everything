-- link_assertion_consumption: the single-use ledger that makes an FR2 link
-- assertion usable exactly once (FR3, NFR1). Keyed on the assertion's
-- `jti`, following the same append-only, PRIMARY-KEY-is-the-guarantee
-- pattern mcp_auth_code already establishes (migration 007,
-- libs/go/mcpauth/authcode.go) -- but deliberately a SEPARATE table rather
-- than a reuse of mcp_auth_code, because the two ledgers answer different
-- questions:
--
--   * Different issuer: mcp_auth_code's rows are minted by ASS's own
--     GET /authorize (an OAuth2 authorization code ASS itself issues to
--     an MCP client). A link assertion's `jti` is minted by whichever
--     issuer signed the FR2 assertion being redeemed -- a value ASS never
--     generates, only ever verifies and consumes.
--   * Different lifecycle: an auth code is redeemed by POST /token as one
--     step of an OAuth2 authorization-code exchange and its row's columns
--     (client_id, redirect_uri, code_challenge, ...) exist to support that
--     exchange. A link assertion has already been verified (signature,
--     audience, expiry) by the time this table is touched -- consumption
--     here is purely "has this jti been redeemed before", nothing else.
--   * Different auth path: mcp_auth_code backs the MCP bearer-credential
--     OAuth2 front end (`web`'s /authorize, /token, /register). This table
--     backs the FR2/FR3 link-assertion flow, an unrelated caller-identity
--     path with no OAuth2 client/redirect/PKCE concepts of its own.
--
-- Sharing one table across both would conflate two different callers'
-- replay-protection semantics under a single schema, so this migration
-- adds a new table instead of widening mcp_auth_code.
--
-- Not SCD2 (AGENTS.md § SCD2 explicitly excludes append-only event logs):
-- a consumption row is never updated once inserted, so there is no
-- valid_from/valid_to to track -- the PRIMARY KEY on jti *is* the
-- single-use guarantee, enforced by a unique-violation on INSERT, never a
-- SELECT-then-INSERT check (which races).
CREATE TABLE link_assertion_consumption (
    jti         TEXT PRIMARY KEY,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Supports reaping consumed rows once the assertion they refer to could no
-- longer be valid anyway, mirroring mcp_auth_code_expires_at (migration
-- 007) for the same reason.
CREATE INDEX link_assertion_consumption_expires_at
    ON link_assertion_consumption(expires_at);
