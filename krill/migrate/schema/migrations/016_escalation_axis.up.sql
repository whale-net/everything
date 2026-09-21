-- 016_escalation_axis: the whole M5 escalation/intervention/note-lifecycle
-- axis in one migration (issue #2868, root plan #2851, FR1-FR12,
-- NFR1-NFR6) -- task_escalation_event, task_intervention_event,
-- task_note_lifecycle_event, plus additive columns on task and task_note.
-- Every other M5 task after this one lands independently on trunk and
-- states "no new migration", implementing store/handler/MCP/test code
-- against these tables -- see this migration's own issue body ("Why this
-- task") for why a per-task migration number would risk golang-migrate
-- version gaps, the same rationale 015_work_axis gave for M4.
--
-- Every table below carries `scope_id UUID NOT NULL REFERENCES scope(id)`
-- and a `scope_id` index (NFR1), and both LB4 subject pairs --
-- `created_by_acting_{iss,sub,kind}` / `created_by_on_behalf_of_{iss,sub,kind}`
-- -- NOT NULL (NFR3): every write path this milestone adds always has a
-- real caller session, the same "mandatory pair" call 015's `task` table
-- made for the work axis.
--
-- No table here is SCD2 -- no `valid_from`/`valid_to` anywhere, no
-- in-place update of any event row (NFR2, AGENTS.md's SCD2 section): each
-- is a plain append-only log, same shape as 015's `task_dependency`/
-- `task_lease_event`/`task_attempt`. `task` and `task_note` keep 015's
-- append-only-plus-current-value shape -- this migration only widens the
-- set of columns each holds a "current value" in.
--
-- Non-breaking on landing: at the moment this lands no row is escalated
-- or cancelled, so the tightened `task_claimable_idx` below and the new
-- `ClaimTask` application-level checks (Implementation phase,
-- krill/store/task_claim.go) change no existing behaviour for any row
-- this migration itself writes -- it writes none.
--
-- ============================================================================
-- LB3 -- task_escalation_event: append-only, one row per escalation (FR2, FR3, FR9)
-- ============================================================================
-- Recorded automatically by a thrash-cap `complete` (FR2), an
-- attempt-cap `ReclaimExpired`/`AbandonClaim`/`release` (FR3, FR8), or
-- manually by `escalate` (FR9) -- `reason` is exactly the three values
-- Assumption 4 names, no others. `counter_value`/`cap_value` capture the
-- automatic reasons' triggering counter and cap (FR5); both are NULL for
-- `manual`, which has no causing counter (FR9, NFR4) -- the CHECK below
-- makes that pairing a DB-enforced invariant, not just a store-layer
-- convention, mirroring 015's `task_claim`
-- `(released_at IS NULL) = (release_reason IS NULL)` pattern.
--
-- **No resolution column, on purpose**: an escalation is never rewritten
-- to say how it ended (NFR2, NFR5) -- `requeue`/`cancel` resolve it by
-- clearing `task.current_escalation_id` and appending a
-- `task_intervention_event` (below), never by touching this row.
-- `task_id` is a real DB-enforced REFERENCES onto `task(id)` (015's
-- `task` is not SCD2, so its `id` is table-wide stable, the same
-- reasoning 015's own `task_dependency` note gives for referencing it
-- directly).
CREATE TABLE task_escalation_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    reason                       TEXT        NOT NULL CHECK (reason IN ('thrash-cap', 'attempt-cap', 'manual')),
    counter_value                INT         NULL,
    cap_value                    INT         NULL,
    lane_at_escalation           TEXT        NOT NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (reason = 'manual' AND counter_value IS NULL AND cap_value IS NULL)
        OR (reason <> 'manual' AND counter_value IS NOT NULL AND cap_value IS NOT NULL)
    )
);

CREATE INDEX task_escalation_event_scope_idx ON task_escalation_event(scope_id);

-- Backs "this task's escalation history" (FR5's summary row draws its
-- reason/counter/cap from the most recent one; M4 FR10's per-task fetch
-- shows every one), ordered newest-first like 015's own event-history
-- indexes.
CREATE INDEX task_escalation_event_task_idx ON task_escalation_event(task_id, created_at DESC);

-- ============================================================================
-- LB3 -- task_intervention_event: append-only, one row per operator action (FR6-FR9)
-- ============================================================================
-- Every operator-driven mutation this milestone adds -- requeue (FR6),
-- cancel (FR7), release (FR8), manual escalate (FR9) -- appends exactly
-- one row here, backing NFR3's "who requeued this, on whose behalf" and
-- FR10's cancellation-history query. `escalation_event_id` is set by a
-- `requeue` action to name the specific escalation it resolved (FR6); it
-- is NULL for every other action, including `escalate`, whose own
-- resulting escalation is the freshly-inserted `task_escalation_event`
-- row this call also writes, not a reference back to itself. `reason` is
-- free-text and optional -- an operator's rationale, not a fixed
-- vocabulary like `task_escalation_event.reason`.
CREATE TABLE task_intervention_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    action                       TEXT        NOT NULL CHECK (action IN ('requeue', 'cancel', 'release', 'escalate')),
    escalation_event_id          UUID        NULL REFERENCES task_escalation_event(id),
    reason                       TEXT        NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX task_intervention_event_scope_idx ON task_intervention_event(scope_id);
CREATE INDEX task_intervention_event_task_idx ON task_intervention_event(task_id, created_at DESC);

-- ============================================================================
-- LB3 -- task_note_lifecycle_event: append-only, one row per status transition (FR11)
-- ============================================================================
-- The authoritative history behind `task_note.current_status` (below) --
-- any persona can transition a note's status (FR11), and each transition
-- is an appended row, never an in-place rewrite of a prior transition.
-- `note_id` is a real DB-enforced REFERENCES onto `task_note(id)` -- 015's
-- `task_note` is a flat immutable append (its own LB3 note), so its `id`
-- is stable from the moment it is inserted.
CREATE TABLE task_note_lifecycle_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    note_id                      UUID        NOT NULL REFERENCES task_note(id),
    status                       TEXT        NOT NULL CHECK (status IN ('noted', 'carried-over', 'deferred', 'closed')),
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX task_note_lifecycle_event_scope_idx ON task_note_lifecycle_event(scope_id);
CREATE INDEX task_note_lifecycle_event_note_idx ON task_note_lifecycle_event(note_id, created_at DESC);

-- ============================================================================
-- task: additive columns for FR1 (thrash counter), FR2/FR3/FR6/FR9 (current escalation), FR7 (dead-letter)
-- ============================================================================
-- `thrash_count` is FR1's lane-thrash counter, fed and reset
-- independently of `attempt_count` (NFR4) -- both live on the same
-- append-only-plus-claimed row 015 already established, following the
-- same "derived current value on the parent, history on a sibling
-- append-only table" shape `attempt_count` itself set.
--
-- `current_escalation_id` is a plain `UUID NULL`, not a DB-enforced
-- `REFERENCES task_escalation_event(id)` -- the same store-layer-
-- enforcement call 015 makes for `current_claim_id` (its own note above):
-- an escalation's own lifecycle (set by an escalation, cleared by
-- requeue/cancel) is store-layer business logic, and FR9's one-event
-- rule ("at most one active escalation per task") is enforced by
-- `recordEscalationTx` (Implementation phase), not a table-level FK.
--
-- `cancelled_at` is FR7's dead-letter terminal state -- deliberately not
-- folded into `current_lane` (015's five-lane CHECK), since a cancelled
-- task is not "in" any lane, it is permanently outside the lane sequence
-- entirely, distinct from `Done` (M4 FR8's own terminal state).
ALTER TABLE task
    ADD COLUMN thrash_count          INT         NOT NULL DEFAULT 0,
    ADD COLUMN current_escalation_id UUID        NULL,
    ADD COLUMN cancelled_at          TIMESTAMPTZ NULL;

-- ============================================================================
-- task_note: additive current_status column for FR11's lifecycle
-- ============================================================================
-- The denormalized current value 015's own `task_note` LB3 comment
-- explicitly anticipates ("M5's C26 adds the noted -> carried over /
-- deferred / closed lifecycle by ADDING columns to this same table
-- later"). `task_note_lifecycle_event` above is this column's authoritative
-- history; the note's `body` (015) stays the flat immutable append M4
-- FR12 shipped, untouched by this migration.
ALTER TABLE task_note
    ADD COLUMN current_status TEXT NOT NULL DEFAULT 'noted' CHECK (current_status IN ('noted', 'carried-over', 'deferred', 'closed'));

-- ============================================================================
-- CHECK-enum widening -- drop-and-recreate pattern established by 010/011/014,
-- never editing 015's own CHECK in place
-- ============================================================================
-- `task_claim.release_reason` gains the three operator-driven force-close
-- reasons FR7 (cancel), FR8 (release), FR9 (escalate) each need,
-- alongside M4's original `complete`/`abandon`/`reclaim`.
ALTER TABLE task_claim
    DROP CONSTRAINT task_claim_release_reason_check,
    ADD CONSTRAINT task_claim_release_reason_check CHECK (
        release_reason IN ('complete', 'abandon', 'reclaim', 'release', 'cancel', 'escalate')
    );

-- `task_attempt.outcome` gains exactly two values: `released` for FR8's
-- manual release (Assumption 2: a manual release counts as an attempt,
-- same as an automatic reclaim) and `force-closed` for FR9's manual-
-- escalate force-close (Assumption 7: escalate's force-close increments
-- the run-attempt counter; requeue does not, and cancel is terminal so
-- the question is moot for it -- neither gets an outcome value here).
-- This is the exact set later M5 tasks must not widen again.
ALTER TABLE task_attempt
    DROP CONSTRAINT task_attempt_outcome_check,
    ADD CONSTRAINT task_attempt_outcome_check CHECK (
        outcome IN ('claimed', 'lapsed', 'abandoned', 'completed', 'released', 'force-closed')
    );

-- ============================================================================
-- task_claimable_idx -- FR2's claimability rule enforced in the index itself
-- ============================================================================
-- FR2 is explicit that "claim must not return an escalated task
-- regardless of claim state ... must be enforced in the
-- `task_claimable_idx` partial index itself, not only in `ClaimTask`'s
-- application-level check, or the rule holds in code but not under
-- concurrency". A task escalated through a normal `complete` has
-- `current_claim_id IS NULL` exactly like any ordinary unclaimed task, so
-- the escalation and cancellation predicates below are additional,
-- independent conditions, not implied by the claim predicate 015 already
-- had.
DROP INDEX task_claimable_idx;
CREATE INDEX task_claimable_idx ON task(scope_id)
    WHERE current_claim_id IS NULL AND current_escalation_id IS NULL AND cancelled_at IS NULL;
