-- 015_work_axis: the whole M4 work axis in one migration (issue #2719,
-- FR1-FR12, C14/C15/C16/C25) -- task, task_dependency, task_claim,
-- task_lease_event, task_attempt, and task_note. Every M4 task after this
-- one lands independently on trunk and states "no new migration",
-- implementing store/handler/MCP/test code against these six tables --
-- see this migration's own issue body ("Why one migration for the whole
-- work axis") for why a per-task migration number would risk
-- golang-migrate version gaps. Mirrors M1's up-front numbering note in
-- schema.go's package doc, scaled to a whole milestone instead of one
-- task's tables.
--
-- Every table below carries `scope_id UUID NOT NULL REFERENCES scope(id)`
-- (NFR1), and every uniqueness constraint and query predicate on them is
-- scope-qualified -- the same rule M1 applied to the spec axis
-- (002_spec_entities.up.sql) and M3 applied to the delivery axis
-- (010_milestone_authoring.up.sql onward).
--
-- No table here is SCD2, and none uses `valid_from`/`valid_to` anywhere
-- (NFR2, AGENTS.md's SCD2 section: "Do not apply SCD2 to append-only
-- event logs" -- the work axis is exactly that shape). Each table's own
-- LB3 boundary comment below states which of the two remaining shapes it
-- is and why: `task` is the one **append-only-plus-claimed** table
-- (updated in place only for claim/lease/lane state, mirroring
-- `tools/app_registry`'s `writeback_outbox`,
-- tools/app_registry/migrate/schema/migrations/004_writeback_outbox.up.sql,
-- cited in krill/product/01-current-state.md); every other table here is
-- a plain append-only log, never overwritten, never deleted.
--
-- No column on any table below is ever derived from, or stores, a git
-- branch name or other external ref (NFR5) -- `task.current_lane` is
-- krill's own stored state, not a value read back from a branch.
--
-- ============================================================================
-- LB3 -- task: append-only-plus-claimed, the one table in this migration updated in place
-- ============================================================================
-- A task's own row is written once at CreateTask and thereafter updated
-- in place for exactly the claim/lease/lane fields a live piece of work
-- mutates as it moves: `current_claim_id`, `lease_expires_at`,
-- `current_lane`, `attempt_count`. This is deliberately NOT the SCD2
-- shape (no `valid_from`/`valid_to`, no superseding row, no history of
-- prior lane values kept on this table itself) and NOT a plain
-- immutable-after-create row either -- it is `writeback_outbox`'s shape:
-- one row per unit of work, claimed and released in place, with every
-- claim/lease/attempt EVENT recorded as a separate append-only row on the
-- sibling tables below rather than as an overwritten column here. That
-- history (who claimed it when, every lease extension, every attempt's
-- outcome) is what `task_claim`/`task_lease_event`/`task_attempt` are
-- for -- `task` itself only ever answers "what is true right now".
--
-- `milestone_id` is the one delivery-axis reference this table carries
-- (NFR7, LB6): a `milestone_ref` row, either kind (`kind='milestone'` or
-- `kind='milepebble'`, both the same table since migration 011 widened
-- `kind`) -- never a `feature`/`requirement` id, so a task can never be
-- pointed at the spec chain directly, only at the delivery container a
-- milestone/milepebble already associates to that chain. The store layer
-- (CreateTask, task.go) enforces FR1's two allowed shapes -- a milepebble,
-- or a milestone with no milepebble cut -- this migration only enforces
-- that the referenced row exists at all.
--
-- `lane_sequence`/`current_lane` model FR1's "ordered subset of
-- {Scaffold, Implementation, Testing, Validation, Done}, lanes skippable"
-- convention -- the same five-lane vocabulary this very issue's own
-- project board swimlanes use. The DB CHECK below only constrains
-- vocabulary (every element of `lane_sequence` is one of the five names,
-- and the array is non-empty) and that `current_lane` is one of the five
-- names -- it cannot express "in canonical order, no duplicates, and
-- `current_lane` is a member of `lane_sequence`" as a single CHECK
-- expression, so that full validation is CreateTask's job (store layer),
-- not this migration's.
--
-- `current_claim_id` is a plain `UUID NULL`, not a DB-enforced
-- `REFERENCES task_claim(id)`: `task_claim` is created later in this same
-- migration, and a claim's own lifecycle (open, then closed via
-- `released_at`) is store-layer business logic (mediated.go/task.go's
-- transactional claim path, later M4 tasks), not something a table-level
-- FK by itself could express -- the same "store-layer enforcement over a
-- DB-level REFERENCES" call LB2 already makes for `milestone_ref`'s own
-- non-unique-across-time parent columns (see krill/store/errors.go's
-- `plainRowExists` doc comment). `lease_expires_at` is likewise plain: it
-- is meaningful only while `current_claim_id` is set, enforced by the
-- store layer's claim/heartbeat/release methods, not a CHECK here.
--
-- The six `created_by_acting_*`/`created_by_on_behalf_of_*` columns are
-- NOT NULL (NFR3, LB4) -- CreateTask is the only write path onto this
-- table, and it always has a real caller session (mirroring
-- `milestone_deferral`'s mandatory columns, migration 010's LB4 note),
-- unlike `milestone_ref`'s own nullable pair, which exists only because
-- of the importer's pre-existing session-less path.
CREATE TABLE task (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    milestone_id                 UUID        NOT NULL REFERENCES milestone_ref(id),
    title                        TEXT        NOT NULL,
    body                         TEXT        NULL,
    lane_sequence                TEXT[]      NOT NULL CHECK (
                                      array_length(lane_sequence, 1) > 0
                                      AND lane_sequence <@ ARRAY['Scaffold', 'Implementation', 'Testing', 'Validation', 'Done']
                                  ),
    current_lane                 TEXT        NOT NULL CHECK (current_lane IN (
                                      'Scaffold', 'Implementation', 'Testing', 'Validation', 'Done'
                                  )),
    attempt_count                INT         NOT NULL DEFAULT 0,
    current_claim_id             UUID        NULL,
    lease_expires_at             TIMESTAMPTZ NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX task_scope_idx ON task(scope_id);
CREATE INDEX task_milestone_idx ON task(milestone_id);

-- Backs FR3's claim query ("find an unclaimed task in this scope"),
-- mirroring writeback_outbox_pending_idx's partial-index precedent for
-- the same "find the next claimable row" shape of query.
CREATE INDEX task_claimable_idx ON task(scope_id) WHERE current_claim_id IS NULL;

-- ============================================================================
-- LB3 -- task_dependency: append-only edge table, not SCD2
-- ============================================================================
-- A dependency declaration is a fact recorded once (FR2) -- there is no
-- "revise a dependency" or "un-depend" operation in this milestone's FR
-- list, so this is a plain append-only table, no `valid_from`/`valid_to`
-- pair, the same boundary call `milestone_deferral` and `entity_milestone`
-- already make for their own edge/fact rows. `task_id`/`depends_on_task_id`
-- ARE real DB-enforced REFERENCES back onto `task(id)` -- `task` is not
-- SCD2 (its own LB3 note above), so its `id` is table-wide stable and
-- unique across every in-place claim/lease/lane update. The unique
-- constraint and the CHECK together make FR2's "declare that a task
-- depends on another" both idempotent-safe (a duplicate edge is rejected,
-- not silently doubled) and self-loop-safe (a task can never depend on
-- itself) at the DB layer, not only by Go validation.
CREATE TABLE task_dependency (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    depends_on_task_id           UUID        NOT NULL REFERENCES task(id),
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (task_id <> depends_on_task_id)
);

CREATE UNIQUE INDEX task_dependency_scope_task_depends_on_idx ON task_dependency(scope_id, task_id, depends_on_task_id);

-- Backs FR3's claimability check the other direction: "does any task
-- depend on me, and has that dependent's own gating already been
-- satisfied" is answered from task_id; "what does this task depend on,
-- and has every one of them reached Done" (the actual FR3 gate) is
-- answered from depends_on_task_id, which the unique index above does not
-- serve as its own leading column.
CREATE INDEX task_dependency_depends_on_idx ON task_dependency(depends_on_task_id);

-- ============================================================================
-- LB3 -- task_claim: append-only claim events, with one narrow in-place exception
-- ============================================================================
-- Claiming a task is an event that accrues (FR3, FR5) -- a row is
-- inserted once per claim attempt that succeeds, and is never deleted.
-- The one in-place update this table permits is claim CLOSURE:
-- `released_at`/`release_reason`, set exactly once when a claim ends
-- (FR8 complete, FR9 abandon, or FR7's reclaim-on-lapse), never re-set or
-- cleared afterward. This is narrower than `task`'s own
-- append-only-plus-claimed shape above -- every other column on this
-- table (`task_id`, `session_id`, the subject pair, `claimed_at`,
-- `initial_lease_expires_at`) is written once at INSERT and never
-- touched again. `task_id` is a real DB-enforced REFERENCES onto
-- `task(id)` (see task_dependency's own note on why that is safe).
-- `session_id` is a real DB-enforced REFERENCES onto `krill_session(id)`
-- (003_session.up.sql) -- a claim is always made by a caller with an
-- active session (FR3), and `krill_session` rows are never deleted or
-- superseded, so referencing it directly is exactly as safe as
-- referencing `task(id)`.
CREATE TABLE task_claim (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    session_id                   UUID        NOT NULL REFERENCES krill_session(id),
    claimed_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    initial_lease_expires_at     TIMESTAMPTZ NOT NULL,
    released_at                  TIMESTAMPTZ NULL,
    release_reason               TEXT        NULL CHECK (release_reason IN ('complete', 'abandon', 'reclaim')),
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((released_at IS NULL) = (release_reason IS NULL))
);

CREATE INDEX task_claim_scope_idx ON task_claim(scope_id);

-- Backs both "this task's current/most recent claim" (FR3's claimability
-- check) and "every claim this task has ever had" (a resumption read,
-- FR10), ordered newest-first.
CREATE INDEX task_claim_task_claimed_idx ON task_claim(task_id, claimed_at DESC);

-- ============================================================================
-- LB3 -- task_lease_event: append-only heartbeat/lease-extension log, never overwritten
-- ============================================================================
-- Every heartbeat (FR6) appends a new row rather than updating a
-- "current lease" column on this table -- `task.lease_expires_at` is
-- that current value (this table's own LB3 note references it), this
-- table is purely the history of how it got there, the same "history
-- lives on a sibling append-only table, the parent row only holds the
-- derived current value" shape `milestone_status_event` established for
-- `milestone_ref` (migration 012). `claim_id` is a real DB-enforced
-- REFERENCES onto `task_claim(id)` -- a heartbeat always belongs to
-- exactly one claim, and a claim row's `id` is stable from the moment it
-- is inserted (this migration's task_claim note). `task_id` is
-- denormalized from `claim_id` (rather than requiring a join through
-- `task_claim` for every read of a task's lease history) -- the same
-- denormalization rationale `writeback_outbox.environment_key` documents
-- in tools/app_registry's own migration.
CREATE TABLE task_lease_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    claim_id                     UUID        NOT NULL REFERENCES task_claim(id),
    extended_to                  TIMESTAMPTZ NOT NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX task_lease_event_scope_idx ON task_lease_event(scope_id);
CREATE INDEX task_lease_event_claim_idx ON task_lease_event(claim_id, created_at DESC);

-- ============================================================================
-- LB3 -- task_attempt: append-only attempt log, never overwritten
-- ============================================================================
-- One row per attempt outcome (FR5 claimed, FR7 lapsed, FR9 abandoned,
-- FR8 completed) -- `task.attempt_count` (this migration's task note) is
-- the derived current count; this table is the durable history behind
-- it, same "sibling append-only log, derived count/current value on the
-- parent" shape as task_lease_event above. `claim_id` is nullable
-- (unlike task_lease_event's, which always has one): FR7's reclaim-past-
-- the-cap path records a `lapsed` attempt for a lease that already
-- expired with no new claim to attach it to, so a claim is not always
-- available at the moment an attempt row is written. When present,
-- `claim_id` is a real DB-enforced REFERENCES onto `task_claim(id)`,
-- same as task_lease_event's.
CREATE TABLE task_attempt (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NOT NULL REFERENCES task(id),
    claim_id                     UUID        NULL REFERENCES task_claim(id),
    outcome                      TEXT        NOT NULL CHECK (outcome IN ('claimed', 'lapsed', 'abandoned', 'completed')),
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX task_attempt_scope_idx ON task_attempt(scope_id);
CREATE INDEX task_attempt_task_idx ON task_attempt(task_id, created_at DESC);

-- ============================================================================
-- LB3 -- task_note: flat, immutable append (FR12), never overwritten, no lifecycle column
-- ============================================================================
-- A note is written once and never revised, resolved, or deleted in this
-- milestone (FR12) -- no status/lifecycle column of any kind, on purpose:
-- M5's C26 adds the noted -> carried over / deferred / closed lifecycle
-- by ADDING columns to this same table later, never by reshaping the
-- verb this migration ships. `kind`'s CHECK constraint below is the one
-- place the fixed note-kind enumeration is authoritative at the DB layer
-- -- krill/store's future note-writing task (issue #2727) mirrors this
-- exact set in a Go enumeration and must keep the two in lockstep (an
-- unknown kind is rejected at both layers, never only one). 'scope-note'
-- (FR11) models today's GitHub `source:scope-note` label convention as a
-- first-class note kind rather than a parallel issue-comment convention;
-- 'comment' is the general-purpose free-form kind every other note that
-- is not scope-discovery falls under. Widening this set later (a new note
-- kind M5 or a later milestone needs) is a new migration's CHECK, mirroring
-- how `milestone_ref.kind` was widened across migrations 010/011/014
-- rather than this migration's own CHECK being edited in place.
--
-- Exactly one target per note (`task_id` alone, or `entity_kind` +
-- `entity_id` together naming a spec-axis entity a task's work touches)
-- -- enforced by the CHECK below, not just documented convention, so a
-- future write path cannot silently create a note attached to both or
-- neither. `entity_kind` is deliberately NOT a DB-enforced REFERENCES
-- (it names one of five different spec-axis tables depending on its own
-- value, which no single FK column can express) -- the store layer
-- (issue #2727's RecordNote) validates that `entity_id` actually names a
-- current row of the table `entity_kind` selects, the same "store-layer
-- enforcement over a DB-level REFERENCES" call LB2 makes for
-- `milestone_ref`'s own cross-shape parent columns. `task_id`, where
-- populated, IS a real DB-enforced REFERENCES onto `task(id)`.
CREATE TABLE task_note (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    task_id                      UUID        NULL REFERENCES task(id),
    entity_kind                  TEXT        NULL CHECK (entity_kind IS NULL OR entity_kind IN (
                                      'product', 'feature_set', 'feature', 'requirement', 'load_bearing_decision'
                                  )),
    entity_id                    UUID        NULL,
    kind                         TEXT        NOT NULL CHECK (kind IN ('scope-note', 'comment')),
    body                         TEXT        NOT NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (task_id IS NOT NULL AND entity_kind IS NULL AND entity_id IS NULL)
        OR
        (task_id IS NULL AND entity_kind IS NOT NULL AND entity_id IS NOT NULL)
    )
);

CREATE INDEX task_note_scope_idx ON task_note(scope_id);
CREATE INDEX task_note_task_idx ON task_note(task_id, created_at) WHERE task_id IS NOT NULL;
CREATE INDEX task_note_entity_idx ON task_note(entity_kind, entity_id, created_at) WHERE entity_kind IS NOT NULL;
