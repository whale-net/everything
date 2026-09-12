-- Migration 017: region hierarchy history + board recorded region (M3)
--
-- Additive schema migration -- schema only, no Go behavior lands here. Two
-- SCD2 history tables per AGENTS.md section SCD2, plus one plain mirror
-- column on board.

-- -- region_parent_history -----------------------------------------------------
-- SCD2 region hierarchy, per FR1. One open row per region at all times, from
-- the moment the region exists -- the backfill below gives every
-- pre-existing region one open row reflecting its current
-- region.parent_region_id.
--
-- parent_region_id is deliberately NULLable, unlike
-- sensor_region_history.region_id NOT NULL: a sensor's unplaced state is the
-- absence of an open row, but a region's top-level-ness must be recorded so
-- ancestor walks terminate on a NULL-parent row, never on a missing row.
-- NULL on an open row means "top-level region".

CREATE TABLE region_parent_history (
    history_id       BIGSERIAL   PRIMARY KEY,
    region_id        BIGINT      NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
    parent_region_id BIGINT      REFERENCES region(region_id) ON DELETE RESTRICT,
    valid_from       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to         TIMESTAMPTZ
);

CREATE INDEX idx_region_parent_history_region_id        ON region_parent_history(region_id);
CREATE INDEX idx_region_parent_history_parent_region_id ON region_parent_history(parent_region_id);
-- Partial index for fast "current parent" lookups, per AGENTS.md section SCD2.
CREATE INDEX idx_region_parent_history_current          ON region_parent_history(region_id) WHERE valid_to IS NULL;

-- -- backfill -------------------------------------------------------------------
-- FR1: every region has at least one open history row from the moment it
-- exists. Exactly one open row (valid_to IS NULL) per pre-existing region,
-- reflecting its current region.parent_region_id -- NULL for top-level
-- regions, which is the state being recorded, not an absence. No backfill is
-- needed for the board tables below: no board has a recorded region yet.

INSERT INTO region_parent_history (region_id, parent_region_id)
SELECT region_id, parent_region_id
FROM region;

-- -- board recorded region -------------------------------------------------------
-- Bookkeeping only: never affects reading attribution (FR10) -- readings
-- snapshot sensor.region_id, never the board's.
--
-- board.region_id is a plain current-value mirror of board_region_history's
-- open row. A board's un-recorded state is the absence of an open history
-- row, and the mirror column is NULL to match -- the inverse of
-- region_parent_history above, where top-level-ness is a recorded NULL
-- rather than an absent row.

ALTER TABLE board ADD COLUMN region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT;

CREATE INDEX idx_board_region_id ON board(region_id);

-- board_id cascades on board deletion, mirroring board_owner_history's
-- precedent for board-attached history; the region FK restricts like every
-- other placement-history reference to region.
CREATE TABLE board_region_history (
    history_id  BIGSERIAL   PRIMARY KEY,
    board_id    BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
    region_id   BIGINT      NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
    valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to    TIMESTAMPTZ
);

CREATE INDEX idx_board_region_history_board_id  ON board_region_history(board_id);
CREATE INDEX idx_board_region_history_region_id ON board_region_history(region_id);
-- Partial index for fast "current recorded region" lookups, per AGENTS.md
-- section SCD2. Non-unique like the other _current partial indexes on
-- placement history; the close-and-open write path keeps at most one open
-- row per board.
CREATE INDEX idx_board_region_history_current   ON board_region_history(board_id) WHERE valid_to IS NULL;
