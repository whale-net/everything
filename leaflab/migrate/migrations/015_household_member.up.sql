-- Migration 015: Household membership SCD2 table
--
-- Creates household_member SCD2 table to track household membership changes over time.
-- Follows SCD2 convention: valid_from / valid_to (no synonyms).
-- Includes open-interval partial index (WHERE valid_to IS NULL) for current members.

-- ── household_member table (SCD2) ────────────────────────────────────────────
-- Tracks membership of principals in households. SCD2-shaped with valid_from/valid_to.

CREATE TABLE household_member (
    member_id    BIGSERIAL PRIMARY KEY,
    household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    principal_id VARCHAR(255) NOT NULL,  -- principal identifier (email or ID)
    role         VARCHAR(64) NOT NULL,   -- member role (Owner, Grower, Gawker, etc.)
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ
);

-- Partial index for current members (WHERE valid_to IS NULL).
CREATE INDEX idx_household_member_current
    ON household_member(household_id) WHERE valid_to IS NULL;

-- Temporal index for value-at-time-T queries.
CREATE INDEX idx_household_member_temporal
    ON household_member(household_id, valid_from, valid_to);

-- Unique constraint: a principal can only have one active membership per household at a time.
CREATE UNIQUE INDEX idx_household_member_active
    ON household_member(household_id, principal_id) WHERE valid_to IS NULL;
