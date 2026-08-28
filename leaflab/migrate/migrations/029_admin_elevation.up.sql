-- Migration 029: admin_elevation (Phase 2 scaffold)
--
-- 029 is the next free number: v1 branches on plan/1166 reached 027, and v2
-- siblings on this plan have already reached 028 (plan/1166-v2-1368's
-- 028_config_entry_provenance) as of this write -- state this in the PR,
-- since migration numbers have collided across sibling branches on this
-- plan multiple times before.
--
-- Discharges FR10.1's elevation record and FR12's activation of the
-- leaflab-admin realm role: cross-household admin reach is never standing
-- -- it is entered deliberately with a stated reason, is time-boxed (60
-- minutes, configurable, renewable by re-stating a reason), expires
-- automatically, and stamps both the acting admin subject and the target
-- household on every write made under it.
--
-- Short-lived with an explicit expiry, NOT SCD2 (NFR6.3): an elevation is a
-- single bounded episode with a hard end (expiry or explicit EndElevation),
-- not a current-value history to version with valid_from/valid_to. Compare
-- household_membership (015_ownership), which genuinely is SCD2 because
-- membership has no built-in expiry and must support "value at time T".

CREATE TABLE admin_elevation (
    elevation_id         BIGSERIAL PRIMARY KEY,
    admin_subject        TEXT NOT NULL,
    target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    reason                TEXT NOT NULL,
    started_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at            TIMESTAMPTZ NOT NULL,
    ended_at              TIMESTAMPTZ NULL
);

-- FR10.1/FR10.3's hot lookup path: "does admin_subject hold an unexpired
-- elevation against this specific target household right now" -- gates
-- ElevatedScope construction and backs GetElevationStatus's remaining-time
-- read. Partial on ended_at IS NULL (still-open rows only); expires_at is
-- checked against NOW() by the caller at query time, same as every other
-- SCD2-adjacent open-interval index in this schema.
CREATE INDEX idx_admin_elevation_active
    ON admin_elevation(admin_subject, target_household_id, expires_at)
    WHERE ended_at IS NULL;
