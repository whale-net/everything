-- Migration 022: Append-only audit log (FR8, FR9)
--
-- Creates the audit_record table for recording every write and elevated/granted read.
-- Enforces append-only semantics at the database layer via trigger.
-- Audit rows are NOT SCD2-shaped — no valid_to column.
--
-- Columns:
-- - audit_id: unique record identifier
-- - actor_subject: the principal performing the action (NOT restricted to human IdP subjects)
-- - target_household_id: the household being operated on
-- - action: the operation performed (e.g., "claim_board", "update_plant", "grant_access")
-- - entity_type: the kind of entity affected (e.g., "board", "plant", "membership")
-- - entity_id: the specific entity being operated on (nullable for actions not tied to one entity)
-- - occurred_at: when the action happened
-- - reason: optional justification (required for some actions, e.g., membership denial)

CREATE TABLE audit_record (
    audit_id           BIGSERIAL PRIMARY KEY,
    actor_subject      VARCHAR(255) NOT NULL,
    target_household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    action             VARCHAR(64) NOT NULL,
    entity_type        VARCHAR(64) NOT NULL,
    entity_id          BIGINT,
    occurred_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason             TEXT
);

-- Index for household-scoped queries (FR9 activity listing).
CREATE INDEX idx_audit_record_household
    ON audit_record(target_household_id, occurred_at DESC);

-- Index for actor-scoped queries (support, analytics).
CREATE INDEX idx_audit_record_actor
    ON audit_record(actor_subject, occurred_at DESC);

-- ── Append-only enforcement ──────────────────────────────────────────────────
-- Trigger to reject UPDATE and DELETE operations on audit_record.
-- This enforces the append-only constraint at the database layer, not only in application code.

CREATE OR REPLACE FUNCTION enforce_audit_record_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_record is append-only: % not allowed', TG_OP;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_audit_record_append_only
    BEFORE UPDATE OR DELETE ON audit_record
    FOR EACH ROW
    EXECUTE FUNCTION enforce_audit_record_append_only();
