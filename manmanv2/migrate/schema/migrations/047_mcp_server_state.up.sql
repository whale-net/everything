-- Migration 047: MCP server state (idempotency records, confirmation tokens,
-- gamer start/action allowlists). Owned by the MCP server; additive only.

CREATE TABLE mcp_idempotency_record (
    caller_issuer TEXT NOT NULL,
    caller_subject TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (caller_issuer, caller_subject, tool_name, idempotency_key)
);
CREATE INDEX idx_mcp_idempotency_record_created_at ON mcp_idempotency_record (created_at);

CREATE TABLE mcp_confirmation_token (
    token_id TEXT PRIMARY KEY,
    caller_issuer TEXT NOT NULL,
    caller_subject TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    entity_fingerprint TEXT NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '5 minutes',
    consumed_at TIMESTAMPTZ
);
CREATE INDEX idx_mcp_confirmation_token_expires_at ON mcp_confirmation_token (expires_at);

-- Single-use consume (safe across replicas/restarts):
--   UPDATE mcp_confirmation_token SET consumed_at = NOW()
--   WHERE token_id = $1 AND consumed_at IS NULL AND expires_at > NOW();
-- Zero rows affected means unknown, already used, or expired.

CREATE TABLE mcp_gamer_start_allowlist (
    id BIGSERIAL PRIMARY KEY,
    deployment_id BIGINT NOT NULL,
    granted_by TEXT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to TIMESTAMPTZ
);
CREATE UNIQUE INDEX uq_mcp_gamer_start_allowlist_current
    ON mcp_gamer_start_allowlist (deployment_id) WHERE valid_to IS NULL;

CREATE TABLE mcp_gamer_action_allowlist (
    id BIGSERIAL PRIMARY KEY,
    deployment_id BIGINT NOT NULL,
    action_name TEXT NOT NULL,
    granted_by TEXT NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to TIMESTAMPTZ
);
CREATE UNIQUE INDEX uq_mcp_gamer_action_allowlist_current
    ON mcp_gamer_action_allowlist (deployment_id, action_name) WHERE valid_to IS NULL;
