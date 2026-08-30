-- Ownership shape for LB1/NFR5/NFR6: local identity, SCD2 board ownership,
-- and plain current-value owner references on region/plant.
--
-- Nothing in this migration populates a row anywhere -- leaflab_user rows are
-- created only by interactive sign-in (FR2), board ownership rows only by
-- the claim flow (C25), and region/plant owner columns by nothing in M1.

-- -- leaflab_user ---------------------------------------------------------------
-- Local identity row keyed on the OIDC `sub` claim. Every later ownership
-- reference points at leaflab_user_id, never at a raw claim string (NFR5).

CREATE TABLE leaflab_user (
    leaflab_user_id     BIGSERIAL   PRIMARY KEY,
    oidc_sub            TEXT        NOT NULL UNIQUE,
    preferred_username  TEXT,
    email               TEXT,
    display_name        TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- -- board_owner_history ---------------------------------------------------------
-- SCD2 board ownership, per NFR5 and the repo-wide convention in AGENTS.md
-- section SCD2. Unowned is expressed by the absence of an open row -- no row is
-- inserted at board registration, and nothing backfills an owner for
-- existing boards. leaflab_user_id is NOT NULL on the row; "no owner" is
-- never expressed via a NULL owner on an open row.

CREATE TABLE board_owner_history (
    board_owner_history_id BIGSERIAL   PRIMARY KEY,
    board_id               BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
    leaflab_user_id        BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id),
    valid_from             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to               TIMESTAMPTZ
);

CREATE INDEX idx_board_owner_history_board_id ON board_owner_history(board_id);
-- Partial unique index over the open interval only -- a table-wide
-- UNIQUE (board_id) would forbid the close-and-open that C25's
-- release/re-claim needs.
CREATE UNIQUE INDEX idx_board_owner_history_current
    ON board_owner_history(board_id) WHERE valid_to IS NULL;

-- -- region / plant owner references ---------------------------------------------
-- Plain current-value columns, per NFR6. Deliberately not history tables --
-- do not generalise board_owner_history's shape onto these. Nullable, and
-- in M1 always NULL: nothing in this milestone writes a region or plant row,
-- and no screen displays an owner.

ALTER TABLE region ADD COLUMN owner_leaflab_user_id BIGINT REFERENCES leaflab_user(leaflab_user_id);
ALTER TABLE plant  ADD COLUMN owner_leaflab_user_id BIGINT REFERENCES leaflab_user(leaflab_user_id);

CREATE INDEX idx_region_owner ON region(owner_leaflab_user_id);
CREATE INDEX idx_plant_owner  ON plant(owner_leaflab_user_id);
