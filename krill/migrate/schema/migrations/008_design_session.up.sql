-- 008_design_session (issue #2542, krill M2, FR1-FR4/NFR1): the storage
-- foundation for a recorded design session -- `design_session`, the
-- longer-lived container a session's rounds accumulate under, and
-- `revision_event`, the append-only log of those rounds.
--
-- ============================================================================
-- design_session vs krill_session -- do not confuse the two
-- ============================================================================
-- `design_session` is explicitly NOT `krill_session` (migration 003). A
-- `krill_session` row is the write-gate row FR3's `init` mints per call:
-- one fixed acting/on-behalf-of pair, gating exactly one mutating
-- request. A `design_session` spans many separate `krill_session`-gated
-- calls from potentially different actors over its lifetime -- a
-- producer-role Agent's `draft`, an architect's `reconciliation`, a
-- Requirement Contributor's `answer` are three different calls, each
-- gated by its own, distinct `krill_session`, all accumulating onto the
-- same `design_session`. See krill/ARCHITECTURE.md's "design_session vs
-- krill_session" section for the full writeup.
--
-- opened_by_krill_session_id below is PROVENANCE ONLY -- see that
-- column's comment for why it is never the source of a later
-- revision_event's identity.
CREATE TABLE design_session (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                UUID        NOT NULL REFERENCES scope (id),
    -- product_id holds the parent Product's immutable `id` (see
    -- krill/ARCHITECTURE.md's "The spec entity model" section) -- not a
    -- REFERENCES, for the same reason no other spec-entity parent column
    -- is one: `product.id` is not unique table-wide (it is SCD2).
    -- krill/store's currentRowExists helper enforces parent existence at
    -- write time, inside the same transaction as this table's INSERT,
    -- same as every other child entity's Create.
    product_id              UUID        NOT NULL,
    -- FR8's plain-language idea/user story that opened the session.
    -- Deliberately a column here, NOT a sixth event_type value on
    -- revision_event and NOT a `draft` event with empty deltas: FR2's
    -- event_type enum is closed at exactly five values (draft,
    -- reconciliation, answer, signoff, ruling), none of which names a
    -- Requirement Contributor's raw, pre-entity idea, and widening that
    -- enum for this would contradict the plan that fixes it. Reusing
    -- `draft` would also misattribute a contributor's plain language as a
    -- producer-role Agent's proposal -- FR9/FR10 make `draft` specifically
    -- the Agent's act. No krill entity reference is required in this
    -- text (FR8).
    opening_submission      TEXT        NOT NULL,
    -- FR1: the krill_session that gated the `open` call. Provenance
    -- only -- never the source of a later revision_event's identity.
    -- FR2 requires every revision_event to record its own acting/
    -- on-behalf-of pair directly on that event's row; a reader must never
    -- resolve a revision_event's attribution by following this column
    -- back to design_session and then to krill_session.
    opened_by_krill_session_id UUID     NOT NULL REFERENCES krill_session (id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Boundary call (LB3): design_session is a plain, append-only container
-- row, mirroring krill_session's (003_session.up.sql) and
-- pointer_artifact's (005_pointer_artifact.up.sql) own LB3 calls. NOT
-- SCD2 (AGENTS.md "SCD2": no valid_from/valid_to pair) -- M2 ships no
-- update path over this row; its mutable state (how many rounds it has
-- seen, what those rounds said) is entirely derived from its
-- revision_event log, never stored back onto this row.
CREATE INDEX ON design_session (scope_id);
CREATE INDEX ON design_session (product_id);

-- ============================================================================
-- revision_event: the append-only round log (FR2, FR3, FR4, NFR1)
-- ============================================================================
CREATE TABLE revision_event (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                UUID        NOT NULL REFERENCES scope (id),
    -- A real FK here is correct, unlike product_id above: design_session.id
    -- IS unique table-wide (design_session is not SCD2).
    session_id              UUID        NOT NULL REFERENCES design_session (id),
    -- Monotonically increasing per session, allocated by krill/store under
    -- a row lock on the owning design_session (see design_session.go's
    -- Append doc comment) -- never a plain MAX(seq_no)+1 without that
    -- lock, which would race this UNIQUE index under concurrent appends.
    seq_no                  INT         NOT NULL,
    -- LB4 two-triple shape, copied verbatim from krill_session (migration
    -- 003): acting is who made the call that produced this event,
    -- on_behalf_of is who it is attributed to. Sourced from whichever
    -- krill_session gated that call -- a self-acting event writes both
    -- triples identically, exactly as M1's InitSession callers already do.
    acting_iss              TEXT        NOT NULL,
    acting_sub              TEXT        NOT NULL,
    acting_kind             TEXT        NOT NULL CHECK (acting_kind IN ('human', 'service')),
    on_behalf_of_iss        TEXT        NOT NULL,
    on_behalf_of_sub        TEXT        NOT NULL,
    on_behalf_of_kind       TEXT        NOT NULL CHECK (on_behalf_of_kind IN ('human', 'service')),
    event_type              TEXT        NOT NULL CHECK (event_type IN ('draft', 'reconciliation', 'answer', 'signoff', 'ruling')),
    -- List of {entity_id, change, summary_line}. `change` is
    -- created|updated ONLY -- no `deleted`. M1's entity model has no
    -- delete/retire operation (only Create and the SCD2 close-and-open
    -- Amend, per krill/ARCHITECTURE.md's "What this task does not build"
    -- and "Amend and as-of history reads" sections), and no M2 FR
    -- retracts a proposed entity. A retire/delete op and whatever
    -- `change` value would name it is unscoped future work.
    entity_deltas           JSONB       NOT NULL DEFAULT '[]'::jsonb,
    -- {opened: [...], resolved: [...]} by question id.
    open_questions_delta    JSONB       NOT NULL DEFAULT '{"opened":[],"resolved":[]}'::jsonb,
    -- FR3: the repo commit SHA (or equivalent doc reference) the round
    -- was checked against. Conditionally required below.
    verified_against        TEXT        NULL,
    -- FR4: no free-text signoff column exists -- FR4 is explicit that a
    -- signoff does not accept free text in place of this enum.
    -- Conditionally required below.
    signoff_status          TEXT        NULL CHECK (signoff_status IS NULL OR signoff_status IN ('approved', 'changes_requested')),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- FR3: verified_against is required for draft/reconciliation rounds
    -- (the two round kinds a "checked against the repo" claim applies to)
    -- and forbidden otherwise.
    CONSTRAINT revision_event_verified_against_ck CHECK (
        (event_type IN ('draft', 'reconciliation') AND verified_against IS NOT NULL)
        OR (event_type NOT IN ('draft', 'reconciliation') AND verified_against IS NULL)
    ),
    -- FR4: signoff_status is required for a signoff round and forbidden
    -- otherwise.
    CONSTRAINT revision_event_signoff_status_ck CHECK (
        (event_type = 'signoff' AND signoff_status IS NOT NULL)
        OR (event_type != 'signoff' AND signoff_status IS NULL)
    ),

    CONSTRAINT revision_event_session_seq_no_uq UNIQUE (session_id, seq_no)
);

-- Boundary call (LB3): revision_event is an APPEND-ONLY EVENT LOG -- not
-- SCD2 and not a mutable row, mirroring krill_session's own append-only
-- call (003_session.up.sql). NFR1: no update or delete path exists for a
-- recorded event, anywhere in krill/store -- there is therefore no
-- valid_from/valid_to pair and no updated_at; a row is written once by
-- Append and never changed.
CREATE INDEX ON revision_event (scope_id);
