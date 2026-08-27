-- Migration 021: possession-challenge claim schema (Phase 2 scaffold, FR76)
--
-- 021 is the next free number visible in this worktree (020 is the last
-- migration reachable from plan/1166-v2-1341/1339/1331, this task's merge
-- base); state in the PR since sibling v2 branches on plan/1166 have
-- collided on migration numbers before. At least one sibling branch not yet
-- merged here (plan/1166-v2-1361, commit 15e534ce) has already claimed 021
-- for a different table -- flagging now so the collision is expected and
-- renumbered at merge time rather than discovered as a surprise, matching
-- 020's precedent (which renumbered 017 -> 020 for the same reason).
--
-- Schema for FR76's self-service board claim: a board is claimable by an
-- authenticated principal who discharges a possession challenge (r >= 2
-- challenger-marked device restarts, each observed within a short bound of
-- an instant the challenger marked) when the board is never-claimed or
-- resolves to the member-less Unadopted household (migration 015).
--
-- Short-lived, expiring tokens -- explicitly NOT SCD2 (NFR6.3): no
-- valid_to column anywhere in this migration. A challenge/round/cooldown
-- row's lifecycle is tracked by state/closed_at/until columns instead, per
-- the issue's Scaffold section.
--
-- claim_challenge_round carries a denormalized device_id (copied from its
-- parent claim_challenge at round-creation time), which is NOT in the
-- issue's literal Scaffold column list. This is the flagged residual-risk
-- item the issue calls out for SB-2.2 ("its residual risk concentrates in
-- ... the discharge evidence class"): the Implementation section requires
-- "a restart signal may satisfy at most one round, ever. Enforce with a
-- uniqueness constraint on the satisfying evidence, not application logic
-- alone." For the reading-evidence class that's a trivial partial-unique
-- index on satisfied_by_reading_id (a reading row already has a stable,
-- globally unique id). For the non-retained-manifest exception there is no
-- such natural id -- satisfied_by_manifest_at is only a timestamp, and the
-- physical evidence event (one non-retained manifest delivery for a given
-- device_id) is not scoped to a single challenge: two different principals
-- could each be mid-challenge against the same never-read device_id, and
-- both would observe the same manifest delivery. Without device_id on this
-- table, satisfied_by_manifest_at alone cannot be uniquely constrained
-- against being claimed by two different rounds (whether in the same or
-- different challenges), which would silently violate "at most one round,
-- ever" for the manifest evidence class specifically. Denormalizing
-- device_id here is the minimal schema change that lets a real database
-- constraint (not application logic) cover both evidence classes
-- symmetrically -- see the two partial unique indexes below.

CREATE TABLE claim_challenge (
    challenge_id      BIGSERIAL PRIMARY KEY,
    -- Opaque external handle returned to the caller by OpenClaimChallenge.
    -- Never the numeric challenge_id -- an incrementing id would leak
    -- challenge volume/ordering across principals.
    handle             TEXT NOT NULL UNIQUE,
    principal_subject  TEXT NOT NULL,
    -- Syntactically-valid device_id as submitted at open time. Deliberately
    -- NOT a foreign key to board(device_id): requirement 1 (uniform
    -- initiation) requires OpenClaimChallenge to succeed identically for a
    -- device_id that does not exist at all, so this column must be able to
    -- name a board row that never exists.
    device_id          TEXT NOT NULL,
    -- A28: r >= 2, configurable (leaflab/api/claim.Config).
    rounds_required    INT NOT NULL,
    rounds_satisfied   INT NOT NULL DEFAULT 0,
    attempts_used      INT NOT NULL DEFAULT 0,
    opened_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at         TIMESTAMPTZ NOT NULL,
    -- 'open': still within its bounded lifetime and attempt budget.
    -- 'discharged': rounds_satisfied reached rounds_required. Per
    -- requirement 6, reaching 'discharged' does NOT by itself mean the
    -- claim succeeded (CompleteClaim still refuses for a board owned by a
    -- real household) -- 'discharged' here records the challenge-round
    -- fact only, never the claim outcome.
    -- 'not_discharged': lifetime or attempt budget exhausted without
    -- reaching rounds_required (requirement 5).
    state              TEXT NOT NULL DEFAULT 'open'
                           CHECK (state IN ('open', 'discharged', 'not_discharged')),
    -- Recorded server-side the instant state transitions to 'discharged',
    -- regardless of whether CompleteClaim ever succeeds against it --
    -- requirement 6's "the discharge fact is recorded server-side and is
    -- readable by an elevated admin as FR77's evidence; it is never
    -- disclosed to the challenger."
    discharged_at      TIMESTAMPTZ NULL
);

-- Requirement 2 (rate limiting / oracle-safety): "There is one open
-- challenge per (principal, device_id) pair." Enforced as a real
-- constraint, not application logic, identically whether device_id
-- resolves to anything or not.
CREATE UNIQUE INDEX idx_claim_challenge_open_per_principal_device
    ON claim_challenge(principal_subject, device_id) WHERE state = 'open';

-- OpenClaimChallengeStatus lookup by external handle.
CREATE INDEX idx_claim_challenge_handle ON claim_challenge(handle);

-- FR9 (requirement 7): a household sees, for its own boards, that a
-- possession challenge was opened and whether it was discharged, without
-- identifying the attempting principal. Sweeping expired challenges to
-- 'not_discharged' (requirement 5's bounded-lifetime exhaustion) also scans
-- by device_id.
CREATE INDEX idx_claim_challenge_device_id ON claim_challenge(device_id);

CREATE TABLE claim_challenge_round (
    round_id                    BIGSERIAL PRIMARY KEY,
    challenge_id                BIGINT NOT NULL REFERENCES claim_challenge(challenge_id) ON DELETE CASCADE,
    -- Denormalized from claim_challenge.device_id -- see this migration's
    -- doc comment for why (uniqueness of the manifest evidence class).
    device_id                   TEXT NOT NULL,
    round_index                 INT NOT NULL,
    -- Set by MarkClaimRound (requirement 3): the instant the claimant
    -- marked the start of this round.
    t0                          TIMESTAMPTZ NOT NULL,
    -- t0 + the configured per-round bound (A28: 3 minutes, configurable).
    -- "Round n+1's bound opens only after round n closes" -- this column
    -- exists only once the round itself has been marked, never computed in
    -- advance for a round that hasn't started.
    bound_expires_at             TIMESTAMPTZ NOT NULL,
    -- Evidence class 1 (requirement 4): an observed uptime_s regression on
    -- a reading from this board. NULL when this round is satisfied by the
    -- manifest exception instead, or not yet satisfied at all.
    satisfied_by_reading_id      BIGINT NULL REFERENCES sensor_reading(reading_id),
    -- Evidence class 2 (requirement 4's narrow exception): a non-retained
    -- DeviceManifest observed after t0, for a device_id from which no
    -- reading has ever been received. NULL when this round is satisfied by
    -- a reading instead, or not yet satisfied at all.
    satisfied_by_manifest_at     TIMESTAMPTZ NULL,
    -- Which evidence class applied -- recorded server-side for FR77's
    -- audit trail, but "never disclosed to the caller" (requirement 4).
    -- 'uptime_regression' | 'manifest_exception'.
    evidence_class                TEXT NULL
                                      CHECK (evidence_class IN ('uptime_regression', 'manifest_exception')),
    closed_at                     TIMESTAMPTZ NULL,
    -- Exactly one of the two evidence columns may be set, and only when
    -- evidence_class names the matching class -- a database-level guard
    -- against a round being satisfied by both, or by neither while still
    -- carrying an evidence_class.
    CONSTRAINT chk_claim_challenge_round_evidence_consistency CHECK (
        (evidence_class IS NULL AND satisfied_by_reading_id IS NULL AND satisfied_by_manifest_at IS NULL)
        OR (evidence_class = 'uptime_regression' AND satisfied_by_reading_id IS NOT NULL AND satisfied_by_manifest_at IS NULL)
        OR (evidence_class = 'manifest_exception' AND satisfied_by_manifest_at IS NOT NULL AND satisfied_by_reading_id IS NULL)
    ),
    UNIQUE (challenge_id, round_index)
);

-- Round bookkeeping (Implementation section): "a restart signal may satisfy
-- at most one round, ever. Enforce with a uniqueness constraint on the
-- satisfying evidence, not application logic alone." A given sensor_reading
-- row can be the satisfying evidence for at most one round, globally,
-- across every challenge.
CREATE UNIQUE INDEX idx_claim_challenge_round_reading_once
    ON claim_challenge_round(satisfied_by_reading_id) WHERE satisfied_by_reading_id IS NOT NULL;

-- Same guarantee for the manifest-exception evidence class, keyed on
-- (device_id, satisfied_by_manifest_at) since there is no reading-row id to
-- anchor to for this class -- see this migration's doc comment.
CREATE UNIQUE INDEX idx_claim_challenge_round_manifest_once
    ON claim_challenge_round(device_id, satisfied_by_manifest_at) WHERE satisfied_by_manifest_at IS NOT NULL;

-- Open-round lookup: given a device_id (from an inbound reading/manifest),
-- find the round(s) currently waiting on evidence for it.
CREATE INDEX idx_claim_challenge_round_device_id_open
    ON claim_challenge_round(device_id) WHERE closed_at IS NULL;

CREATE TABLE claim_cooldown (
    principal_subject  TEXT NOT NULL,
    device_id          TEXT NOT NULL,
    -- Cooldown expiry after a challenge exhausts its lifetime or attempt
    -- budget without discharging (requirement 5). Upserted (extended) by a
    -- new failed challenge against the same (principal, device_id) pair --
    -- a single current row per pair, not history, hence no valid_from/
    -- valid_to (NFR6.3).
    until               TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (principal_subject, device_id)
);

-- ── Board-level restart observation (processor-written watermark) ──────────
--
-- board_uptime_watermark holds the single current uptime_s reading last
-- seen for a board, written by the processor (leaflab/processor/handler.go,
-- Implementation phase) on every reading so the API can detect a regression
-- (requirement 4: "a value both lower than the last recorded uptime_s for
-- the board and small, to account for the uint32 millisecond wrap at ~49.7
-- days"). One current row per board -- an upsert target, not history, hence
-- no valid_from/valid_to (NFR6.3): the prior value is only needed to compare
-- against the newest one, never to reconstruct a timeline.

CREATE TABLE board_uptime_watermark (
    board_id        BIGINT PRIMARY KEY REFERENCES board(board_id) ON DELETE CASCADE,
    last_uptime_s    INT NOT NULL,
    observed_at      TIMESTAMPTZ NOT NULL
);
