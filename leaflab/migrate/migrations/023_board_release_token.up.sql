-- Migration 023: board release token (Implementation phase, FR77(a))
--
-- 023 is the next free number visible in this worktree (022_departure_record
-- is the last migration reachable from this task's merge base); state in the
-- PR since sibling v2 branches on plan/1166 have collided on migration
-- numbers before (020 and 021 both carry a note about exactly this).
--
-- FR77(a): "an explicit release by a member of the losing household" is one
-- of TransferClosure's two evidence branches. ReleaseBoard
-- (leaflab/api/closure.go) issues an opaque, single-use, time-bounded token
-- here that TransferClosure later consumes as proof the losing household
-- consented -- the Scaffold phase's ReleaseBoard skeleton doc comment
-- explicitly left "the release-token issuance and its persistence" for
-- Implementation, which is what this table is.
--
-- Not SCD2 (NFR6.3 does not apply -- this is short-lived, single-use
-- bookkeeping, not history): lifecycle is tracked by used_at/expires_at,
-- the same shape claim_challenge/claim_cooldown (021_claim_challenge.up.sql)
-- use for the FR76 possession-challenge tokens this table's design mirrors.

CREATE TABLE board_release_token (
    release_token_id BIGSERIAL PRIMARY KEY,
    -- Opaque external token returned to the caller by ReleaseBoard. Never
    -- the numeric release_token_id -- same rationale as claim_challenge.handle.
    token             TEXT NOT NULL UNIQUE,
    board_id          BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
    -- The board's owning (losing) household at the moment of release --
    -- TransferClosure requires this to still match the board's current
    -- household before consuming the token, so a token issued before some
    -- other ownership change can't be replayed against a different transfer.
    household_id      BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    released_by       TEXT NOT NULL,
    reason            TEXT,
    issued_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at        TIMESTAMPTZ NOT NULL,
    -- NULL while unconsumed; set the instant TransferClosure consumes it
    -- (single-use -- FR77's evidence must not be replayable).
    used_at           TIMESTAMPTZ
);

-- Token lookup by external token (ReleaseBoard's own opaque handle).
CREATE INDEX idx_board_release_token_token ON board_release_token(token);
-- FR9-shaped owner-facing list: release history for a board, most recent
-- first.
CREATE INDEX idx_board_release_token_board_id ON board_release_token(board_id, issued_at DESC);
