-- Identity core for M1: person, channel, the Persona<->Channel join table
-- (LB2, NFR5), and single-use channel_invite codes. See issue #1568.
--
-- LB2/NFR5: authorization is answered ONLY by reading `role` off
-- channel_person for the (channel_id, person_id) pair with an open
-- (valid_to IS NULL) row -- never off a `channel.owner_id` column (there is
-- none) and never off an assumption that a Channel has exactly one
-- Creator. M1 only ever populates one role=creator and one role=analyst
-- row per Channel, but this schema does not encode that limit.

-- -- person -----------------------------------------------------------------
-- One row per authenticated human, keyed on the Google OAuth `sub` claim --
-- the stable identity key, not email (an email can change or be reused).

CREATE TABLE person (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    google_subject TEXT        NOT NULL UNIQUE,
    email          TEXT,
    display_name   TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- -- channel -----------------------------------------------------------------
-- One row per connected YouTube channel. Deliberately no owner_id column --
-- ownership and every other role lives entirely in channel_person (LB2).

CREATE TABLE channel (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    youtube_channel_id           TEXT        NOT NULL UNIQUE,
    title                        TEXT,
    connection_state             TEXT        NOT NULL CHECK (connection_state IN ('connected', 'needs_reauth')),
    connection_state_changed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- -- channel_person (LB2 join table) -----------------------------------------
-- SCD2 per AGENTS.md "SCD2" so role history (who was ever a creator/analyst
-- on a Channel, and when) is auditable. Every approve/finalize/reconnect/
-- invite/read/write authorization check in M1 reads the open (valid_to IS
-- NULL) row(s) for a (channel_id, person_id) pair here -- see
-- //audience_score_system/store's CanApprove/CanInvite/CanReconnect/
-- CanRead/CanWrite, the only sanctioned entry points for these questions.

CREATE TABLE channel_person (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID        NOT NULL REFERENCES channel(id),
    person_id  UUID        NOT NULL REFERENCES person(id),
    role       TEXT        NOT NULL CHECK (role IN ('creator', 'analyst')),
    valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to   TIMESTAMPTZ
);

-- At most one open role per (channel_id, person_id) pair -- a person cannot
-- simultaneously hold two open rows (e.g. creator and analyst) on the same
-- Channel. Re-adding a role after it's been closed is a new row, not a
-- resurrection of the old one (SCD2 close-and-open).
CREATE UNIQUE INDEX channel_person_channel_id_person_id_current
    ON channel_person(channel_id, person_id) WHERE valid_to IS NULL;

-- Supports "which Channels is this Person associated with" reads (e.g.
-- ListConnected-style joins) without a table scan.
CREATE INDEX channel_person_person_id_current
    ON channel_person(person_id) WHERE valid_to IS NULL;

-- -- channel_invite -----------------------------------------------------------
-- Single-use, high-entropy (crypto/rand-generated) invite codes an
-- existing creator (NFR5: authorized via CanInvite) generates to let
-- another Person accept an analyst role on a Channel (FR5-FR8).

CREATE TABLE channel_invite (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id             UUID        NOT NULL REFERENCES channel(id),
    code                   TEXT        NOT NULL UNIQUE,
    created_by_person_id   UUID        NOT NULL REFERENCES person(id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at            TIMESTAMPTZ,
    consumed_by_person_id  UUID REFERENCES person(id),
    invalidated_at         TIMESTAMPTZ
);

-- At most one live (unconsumed, uninvalidated) code per Channel at a time
-- (FR5) -- Generate must invalidate any prior live code for the Channel in
-- the same transaction that creates a new one.
CREATE UNIQUE INDEX channel_invite_channel_id_live
    ON channel_invite(channel_id) WHERE consumed_at IS NULL AND invalidated_at IS NULL;
