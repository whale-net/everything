-- Migration 024: Claim challenge (FR76)
--
-- Self-service board claim: an authenticated principal discharges a possession
-- challenge against a submitted device_id to move a never-claimed or Unadopted
-- board's ownership (FR70). Uniform initiation (FR76.1) means device_id is
-- stored as submitted and never validated against board existence at open time.
--
-- Short-lived tokens with expiry, NOT SCD2 (NFR6.3) — a challenge is a one-shot
-- artifact with a lifetime bound, not a slowly-changing dimension. Rounds are
-- append-only: a round row is inserted when the claimant marks t0 and updated
-- at most once, by that same round, to record its own satisfaction — rounds are
-- never deleted or renumbered.

-- ── claim_challenge ───────────────────────────────────────────────────────────
-- One row per opened challenge. rounds_required / round_bound_seconds /
-- attempts_per_round are captured from configuration at open time (A28: all
-- constants configurable) so an in-flight challenge is unaffected by a later
-- config change.
CREATE TABLE claim_challenge (
    challenge_id        BIGSERIAL PRIMARY KEY,
    challenge_token     VARCHAR(64) NOT NULL UNIQUE,   -- opaque handle returned to the caller
    device_id           VARCHAR(64) NOT NULL,          -- submitted as-is; may not resolve to a board
    principal           VARCHAR(255) NOT NULL,         -- opaque calling principal, not necessarily an IdP subject
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,          -- challenge lifetime bound (A28)
    rounds_required     INTEGER NOT NULL,              -- r at open time (A28: r >= 2)
    round_bound_seconds INTEGER NOT NULL,              -- per-round bound at open time (A28)
    attempts_per_round  INTEGER NOT NULL,              -- bounded attempts per round at open time (A28)
    status              VARCHAR(16) NOT NULL DEFAULT 'open'
                             CHECK (status IN ('open', 'discharged', 'failed')),
    resolved_at         TIMESTAMPTZ,                   -- when status left 'open'
    -- outcome is for admin/audit only (FR76.7, FR77 evidence) — never disclosed
    -- to the challenger. 'claimed': discharged and ownership moved (never-claimed
    -- or Unadopted board). 'discharged_no_effect': discharged against a board
    -- already owned by a real household — confers nothing, indistinguishable to
    -- the challenger from 'not_discharged'. 'not_discharged': exhausted lifetime
    -- or attempts without a valid discharge.
    outcome             VARCHAR(24)
                             CHECK (outcome IN ('claimed', 'discharged_no_effect', 'not_discharged'))
);

-- One open challenge per (principal, device_id) pair (FR76.2).
CREATE UNIQUE INDEX idx_claim_challenge_open_principal_device
    ON claim_challenge(principal, device_id) WHERE status = 'open';

-- Bounded (not capped-to-one) concurrent open challenges per principal (FR76.2).
CREATE INDEX idx_claim_challenge_open_principal
    ON claim_challenge(principal) WHERE status = 'open';

-- Token lookup on every OpenChallenge/MarkRound/PollChallengeState call.
CREATE UNIQUE INDEX idx_claim_challenge_token ON claim_challenge(challenge_token);

-- Sweep/expire open challenges past their lifetime bound.
CREATE INDEX idx_claim_challenge_expires ON claim_challenge(expires_at) WHERE status = 'open';

-- ── claim_challenge_round ─────────────────────────────────────────────────────
-- Append-only round records: one row per (challenge, round_number, attempt_number).
CREATE TABLE claim_challenge_round (
    round_id         BIGSERIAL PRIMARY KEY,
    challenge_id     BIGINT NOT NULL REFERENCES claim_challenge(challenge_id) ON DELETE CASCADE,
    round_number     INTEGER NOT NULL,                  -- 1-indexed; round n+1 opens only after n closes
    attempt_number   INTEGER NOT NULL DEFAULT 1,        -- bounded attempts per round (A28)
    marked_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),-- t0, when the claimant marked round start
    bound_expires_at TIMESTAMPTZ NOT NULL,               -- marked_at + round_bound_seconds
    satisfied_at     TIMESTAMPTZ,                        -- when a restart signal satisfied this round
    -- evidence_class records which restart-signal class discharged this round
    -- (FR76.4) — for admin/audit only; never disclosed to the challenger.
    evidence_class   VARCHAR(32)
                         CHECK (evidence_class IN ('uptime_regression', 'manifest_no_reading'))
);

CREATE UNIQUE INDEX idx_claim_challenge_round_attempt
    ON claim_challenge_round(challenge_id, round_number, attempt_number);

-- Lookup of the current (latest) round/attempt for a challenge.
CREATE INDEX idx_claim_challenge_round_challenge
    ON claim_challenge_round(challenge_id, round_number DESC, attempt_number DESC);

-- ── claim_cooldown ─────────────────────────────────────────────────────────────
-- Cooldown after an exhausted challenge (FR76.6), keyed on the same
-- (principal, device_id) pair the open-challenge cap uses. Short-lived, not
-- SCD2: the current row is simply overwritten (upserted) on each new cooldown.
CREATE TABLE claim_cooldown (
    principal      VARCHAR(255) NOT NULL,
    device_id      VARCHAR(64) NOT NULL,
    cooldown_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (principal, device_id)
);
