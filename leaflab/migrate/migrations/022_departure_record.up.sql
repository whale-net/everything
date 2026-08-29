-- Migration 022: append-only departure record (Phase 2 scaffold, FR77)
--
-- 022 is the next free number visible in this worktree (021_claim_challenge
-- is the last migration reachable from this task's merge base); state in the
-- PR since sibling v2 branches on plan/1166 have collided on migration
-- numbers before (020 and 021 both carry a note about exactly this).
--
-- FR77: transferring an ownership closure (FR70) between two real
-- households requires entitlement evidence and leaves the losing household
-- a durable, readable record naming what left and when. The record stays
-- with the losing household and does NOT travel with the closure (unlike
-- board_ownership/plant.household_id, which move to the gaining household
-- on transfer) -- a separate table, not a column on board/plant, is what
-- makes "stays behind" structural rather than a convention every transfer
-- call site has to remember. It is rendered through FR1.4's OpaqueRef with
-- respect to the *gaining* household (Implementation-phase wiring), so it
-- names counts and instants, not identifying detail, when read from outside
-- the losing household -- NFR6.3 below only governs its write-side shape.
--
-- NFR6.3: departure_record is append-only and must NOT be given SCD2 shape
-- -- no valid_to column, ever. Same BEFORE UPDATE OR DELETE trigger pattern
-- as audit_log (016_audit_log.up.sql) -- see that migration's doc comment
-- for the two-layer enforcement rationale (trigger fires for every role
-- including the table owner; REVOKE is the second, independent layer).

CREATE TABLE departure_record (
    departure_id         BIGSERIAL PRIMARY KEY,
    losing_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Structured "what left" summary -- board/region/plant ids and any other
    -- closure-shaped detail the Implementation phase's TransferClosure wants
    -- to record. JSONB (not separate columns) because the exact shape is an
    -- Implementation-phase decision; board_count/region_count/plant_count
    -- below are the caller-visible aggregate counts FR1.4's OpaqueRef
    -- rendering actually needs, kept as first-class columns so a query never
    -- has to reach into summary just to answer "how many".
    summary                JSONB NOT NULL,
    board_count            INT,
    region_count           INT,
    plant_count            INT,
    -- The principal whose action produced this departure -- a releasing
    -- member (FR77(a)) or an elevated admin (FR77(b)). Never the challenger
    -- from the FR76 possession challenge that may have supplied the
    -- evidence -- this is the departure record, not the challenge itself.
    actor_subject          TEXT NOT NULL,
    -- Set for an elevated admin transfer (FR77(b)'s "reasoned admin
    -- action"); NULL for a plain member release, which carries its own
    -- reason implicitly (a member released their own household's board).
    reason                  TEXT
    -- No valid_to column. Not SCD2 (NFR6.3).
);

-- FR9-shaped owner-facing list: departure rows for a household, most recent
-- first -- mirrors idx_audit_log_target_household_id_occurred_at.
CREATE INDEX idx_departure_record_losing_household_id_occurred_at
    ON departure_record(losing_household_id, occurred_at DESC);

-- ── Append-only enforcement (NFR6.2, NFR6.3) ─────────────────────────────────
-- Two independent layers, identical rationale to audit_log
-- (016_audit_log.up.sql): a BEFORE UPDATE OR DELETE trigger that raises
-- unconditionally (fires for every role, including the table owner/
-- superuser doing ad-hoc DML), plus an explicit REVOKE of UPDATE/DELETE from
-- CURRENT_USER as the second, independent layer.

CREATE FUNCTION enforce_departure_record_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'departure_record is append-only (NFR6.3): % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_departure_record_append_only
    BEFORE UPDATE OR DELETE ON departure_record
    FOR EACH ROW
    EXECUTE FUNCTION enforce_departure_record_append_only();

REVOKE UPDATE, DELETE ON departure_record FROM CURRENT_USER;
