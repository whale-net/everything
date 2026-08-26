-- Migration 018: Board retired state columns
--
-- Adds retired-state columns to board per FR22: a retired board leaves the
-- reporting population (excluded from FR79 offline counts, FR62 household-wide
-- classification) while remaining resolvable with readings and history intact.
-- Each soft-retired state names the operation that sets it and records the
-- acting principal.

-- ── Add retired state columns to board ────────────────────────────────────────

ALTER TABLE board ADD COLUMN retired_at       TIMESTAMPTZ;
ALTER TABLE board ADD COLUMN retired_operation VARCHAR(64);
ALTER TABLE board ADD COLUMN retired_principal VARCHAR(255);

-- NULL retired_at means the board is active (default).
-- retired_at IS NOT NULL means the board is retired.
-- Partial index for finding active boards (common query).
CREATE INDEX idx_board_active ON board(board_id) WHERE retired_at IS NULL;
