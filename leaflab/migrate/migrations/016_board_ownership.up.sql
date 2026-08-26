-- Migration 016: Board ownership (SCD2 + current value on board)
--
-- Adds household_id to board table (current ownership) and creates board_ownership
-- SCD2 history table to track ownership changes over time.
--
-- Design choice: household_id on board provides O(1) current-value access;
-- board_ownership SCD2 table tracks the full ownership history.

-- ── Add household_id to board table ──────────────────────────────────────────

ALTER TABLE board ADD COLUMN household_id BIGINT;

-- All existing boards are assigned to Unadopted at migration time.
-- This is done in a later migration (019) during the backfill phase.
-- For now, household_id is nullable to avoid constraint violations during setup.

-- ── board_ownership table (SCD2) ─────────────────────────────────────────────
-- Tracks board ownership changes over time. SCD2-shaped with valid_from/valid_to.

CREATE TABLE board_ownership (
    ownership_id BIGSERIAL PRIMARY KEY,
    board_id     BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
    household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ
);

-- Partial index for current ownership (WHERE valid_to IS NULL).
CREATE INDEX idx_board_ownership_current
    ON board_ownership(board_id) WHERE valid_to IS NULL;

-- Temporal index for value-at-time-T queries.
CREATE INDEX idx_board_ownership_temporal
    ON board_ownership(board_id, valid_from, valid_to);

-- Unique constraint: a board can only have one active owner at a time.
CREATE UNIQUE INDEX idx_board_ownership_active
    ON board_ownership(board_id) WHERE valid_to IS NULL;
