-- Migration 016: append-only audit log (Phase 2)
--
-- 016 is the next free number after 015 (ownership); state in the PR since
-- sibling v2 branches on plan/1166 have collided on migration numbers
-- before.
--
-- Discharges FR8 (append-only audit): every write, and every read performed
-- under an elevated or granted (non-member) identity, produces an
-- append-only record carrying actor subject, target household, action,
-- entity, timestamp and reason where required. Phase 1's NFR12 operational
-- logging (leaflab/api/logging_interceptor.go) was only an interim stand-in
-- and does not discharge FR8.
--
-- NFR6.3: audit rows are append-only and must NOT be given SCD2 shape -- no
-- valid_to column, ever.
--
-- The BEFORE UPDATE OR DELETE trigger and REVOKE below are the
-- Implementation-phase half of NFR6.2's "enforced close to the data, not
-- only in application code", layered onto the Scaffold's table/index DDL
-- above, per 015_ownership's scaffold-then-feat precedent.

CREATE TABLE audit_log (
    audit_id             BIGSERIAL PRIMARY KEY,
    actor_subject        TEXT NOT NULL,
    -- actor_kind exists specifically to satisfy FR8.3 -- "actor" is not
    -- defined as something only a human can be, so a non-human actor (e.g.
    -- a scheduled job or an automated re-send) must be representable.
    actor_kind           TEXT NOT NULL,
    target_household_id  BIGINT NULL REFERENCES household(household_id),
    action                TEXT NOT NULL,
    entity_kind           TEXT NOT NULL,
    entity_id             TEXT NULL,
    reason                TEXT NULL,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    correlation_id        TEXT NULL
    -- No valid_to column. Not SCD2 (NFR6.3).
);

-- FR9's owner-facing list: audit rows for a household, most recent first.
CREATE INDEX idx_audit_log_target_household_id_occurred_at
    ON audit_log(target_household_id, occurred_at DESC);

-- Actor-scoped lookup (e.g. "what has this principal done").
CREATE INDEX idx_audit_log_actor_subject_occurred_at
    ON audit_log(actor_subject, occurred_at DESC);

-- ── Append-only enforcement (NFR6.2, NFR6.3) ─────────────────────────────────
-- Two independent layers, per NFR6.2's "enforced as close to the data as
-- practical, not only in application code":
--
--   1. A BEFORE UPDATE OR DELETE trigger that raises unconditionally. This is
--      the primary layer -- it fires for every role, including the table
--      owner/superuser doing ad-hoc DML, and is the mechanism the Testing
--      phase's load-bearing test exercises.
--   2. An explicit REVOKE of UPDATE/DELETE from CURRENT_USER (the role
--      applying this migration). In this deployment the migration role and
--      the application's runtime DB role are the same identity (DB_USER /
--      PG_DATABASE_URL -- see leaflab/migrate/ENV.md), so CURRENT_USER here
--      *is* "the application role" the requirement names, with no separate
--      role to provision, grant, or keep in sync across environments.
--      Verified empirically: REVOKE UPDATE/DELETE from a table's own owning
--      role does take effect in Postgres -- ownership does not grant an
--      irrevocable bypass of table-level DML privileges (only DDL/ownership
--      operations like DROP/ALTER are inherent and non-revocable).

CREATE FUNCTION enforce_audit_log_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (NFR6.3): % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION enforce_audit_log_append_only();

REVOKE UPDATE, DELETE ON audit_log FROM CURRENT_USER;
