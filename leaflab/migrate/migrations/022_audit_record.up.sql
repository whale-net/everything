-- Migration 022: Audit record table (FR8, FR9)
--
-- Creates the append-only audit_record table for tracking all write operations
-- and non-member reads. Enforces append-only constraint at the database level.
-- This table is shared infrastructure for both general auditing (FR8) and
-- tracking config application skips (FR1.3 in #1192).

-- ── audit_record table (append-only, no SCD2) ────────────────────────────────
-- Every write and every non-member read produces a row. No DELETE or UPDATE allowed.

CREATE TABLE audit_record (
    audit_id        BIGSERIAL PRIMARY KEY,
    actor_subject   VARCHAR(255) NOT NULL,  -- authenticated principal performing the action
    target_household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    action          VARCHAR(64) NOT NULL,   -- e.g., "push_config", "apply_config_region", "skip_config_entry"
    entity_type     VARCHAR(64) NOT NULL,   -- e.g., "sensor", "region", "config_entry"
    entity_id       BIGINT NOT NULL,        -- primary ID of the entity affected
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason          TEXT,                   -- optional explanation (e.g., skip reason)
    -- Additional columns for config application context (supporting FR1.3):
    config_version  BIGINT,                 -- device_config.version being applied
    i2c_address     SMALLINT,               -- hardware address of the affected sensor (if applicable)
    mux_path        JSONB                   -- mux path of the affected sensor (if applicable)
);

-- Append-only trigger: reject UPDATE and DELETE operations.
-- Only INSERT is allowed; all other operations fail at the database level.
CREATE OR REPLACE FUNCTION audit_record_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_record is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;  -- For DELETE and UPDATE triggers, return is ignored
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_record_no_modify
BEFORE UPDATE OR DELETE ON audit_record
FOR EACH ROW EXECUTE FUNCTION audit_record_append_only();

-- Indexes for common query patterns:
-- 1. Find all audit records for a specific household (actor reads their activity).
CREATE INDEX idx_audit_record_household_occurred
    ON audit_record(target_household_id, occurred_at DESC);

-- 2. Find audit records by action (compliance, monitoring).
CREATE INDEX idx_audit_record_action_occurred
    ON audit_record(action, occurred_at DESC);

-- 3. Find audit records for a specific entity (tracing an entity's history).
CREATE INDEX idx_audit_record_entity
    ON audit_record(entity_type, entity_id, occurred_at DESC);

-- 4. Find recent audit records by actor (forensics, admin views).
CREATE INDEX idx_audit_record_actor_occurred
    ON audit_record(actor_subject, occurred_at DESC);
