-- Migration 023: Household grant table
--
-- Creates household_grant table for time-boxed access grants.
-- A grant is a short-lived token-with-expiry shape (NOT SCD2, FR7 NFR6.3).
-- Grants are revocable in one action and expire on their own without requiring a sweeper.

-- ── household_grant table ────────────────────────────────────────────────────
-- Represents a time-boxed grant of write access to a household.
-- Grants are created by members and expire on their own without a sweeper.
-- Revocation is handled by updating revoked_at to NOW() (or past).

CREATE TABLE household_grant (
    grant_id      BIGSERIAL PRIMARY KEY,
    household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE CASCADE,
    granted_by    VARCHAR(255) NOT NULL,           -- principal who created the grant
    grantee       VARCHAR(255) NOT NULL,           -- principal receiving the grant
    expires_at    TIMESTAMPTZ NOT NULL,            -- when the grant expires
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at    TIMESTAMPTZ                      -- when explicitly revoked (NULL = not revoked)
);

-- Index for listing active grants per household. expires_at can't appear in the predicate
-- (NOW() is not IMMUTABLE), so it's an index column instead; callers still filter
-- "expires_at > NOW()" at query time.
CREATE INDEX idx_household_grant_active
    ON household_grant(household_id, expires_at)
    WHERE revoked_at IS NULL;

-- Unique constraint: a grantee can have at most one non-revoked grant per household.
-- expires_at is intentionally excluded from the predicate for the same IMMUTABLE reason above;
-- an expired-but-unrevoked grant still blocks a new one until CreateGrant's caller revokes it.
CREATE UNIQUE INDEX idx_household_grant_unique_active
    ON household_grant(household_id, grantee)
    WHERE revoked_at IS NULL;
