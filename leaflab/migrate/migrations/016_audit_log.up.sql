-- Migration 016: append-only audit log (Phase 2 scaffold)
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
-- Schema-only in this Scaffold: the BEFORE UPDATE OR DELETE trigger that
-- enforces append-only close to the data (NFR6.2) and the REVOKE of
-- UPDATE/DELETE from the application role are Implementation-phase work,
-- layered onto this same migration file (see 015_ownership's
-- scaffold-then-feat precedent).

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
