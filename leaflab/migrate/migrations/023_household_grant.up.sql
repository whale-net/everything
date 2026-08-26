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

-- Index for listing active grants per household (where expires_at > NOW and revoked_at IS NULL).
CREATE INDEX idx_household_grant_active
    ON household_grant(household_id)
    WHERE expires_at > NOW() AND revoked_at IS NULL;

-- Unique constraint: a grantee can have at most one active grant per household.
CREATE UNIQUE INDEX idx_household_grant_unique_active
    ON household_grant(household_id, grantee)
    WHERE expires_at > NOW() AND revoked_at IS NULL;
