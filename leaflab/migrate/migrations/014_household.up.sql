-- Migration 014: Household table and Unadopted seed
--
-- Creates the household table with no members initially, seeds the member-less
-- Unadopted household, and establishes it as the owner of all pre-existing boards,
-- regions, plants, sensors and readings. Post-condition: zero rows resolve to no
-- household (FR70).

-- ── household table ──────────────────────────────────────────────────────────
-- Represents a household (owner entity). Each board, region, plant resolves to
-- exactly one household.

CREATE TABLE household (
    household_id BIGSERIAL PRIMARY KEY,
    name         VARCHAR(255) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed the member-less Unadopted household.
INSERT INTO household (name, created_at)
VALUES ('Unadopted', NOW())
ON CONFLICT DO NOTHING;

-- Create a helper to find Unadopted household_id for use in other migrations.
CREATE OR REPLACE FUNCTION get_unadopted_household_id()
RETURNS BIGINT AS $$
SELECT household_id FROM household WHERE name = 'Unadopted' LIMIT 1;
$$ LANGUAGE SQL STABLE;
