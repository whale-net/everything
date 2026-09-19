-- 012_milestone_status: append-only status history for a `milestone_ref`
-- row of either kind (issue #2685, FR8, FR9, FR12, C28) -- one fixed
-- status set shared verbatim by milestones and milepebbles (FR8, FR9),
-- recorded as a growing history rather than a value overwritten in place.
--
-- ============================================================================
-- LB3 -- milestone_status_event is append-only, NOT SCD2, even though milestone_ref
-- (the table it hangs off) is plain mutable and the spec axis around it is SCD2
-- ============================================================================
-- Every other krill table's LB3 boundary comment (004_milestone_assoc,
-- 010_milestone_authoring, 011_milepebble) draws the "plain mutable vs.
-- SCD2" line. This table draws a third line: it is neither. A status
-- transition is a work-axis mutation (NFR2's "recorded as an addition to
-- history, never an overwrite") -- the same shape LB3 assigns to
-- work-axis tables in general and `tools/app_registry`'s
-- `writeback_outbox` (migrations/004_writeback_outbox.up.sql) ships
-- concretely: no `valid_from`/`valid_to` pair, no "current row" concept
-- baked into the table itself, no UPDATE path, and no DELETE path. A row
-- once written is immutable forever (NFR2's "truncate, never rewind").
-- This is deliberately unlike `milestone_ref` and `milestone_deferral`
-- (both plain mutable, migration 010's LB3 note) and unlike `requirement`/
-- `load_bearing_decision` (SCD2, migration 002) -- the delivery axis's
-- status register is append-only even though the spec axis around it, and
-- the rest of this same table's own parent row, are not.
--
-- Current status is derived, never stored: "not started" is the absence
-- of any row for a milestone_id, not a seeded row written at
-- CreateMilestone/CreateMilepebble time -- see krill/store/
-- milestone_status.go's CurrentStatus doc comment. No `current_status`
-- column is added to `milestone_ref` here or anywhere in this migration.
--
-- `milestone_id` covers milepebbles too (FR9): both kinds are
-- `milestone_ref` rows (migration 011 widened `kind`), so one status
-- register serves both without a second table or a kind-specific column.
-- It IS a real DB-enforced REFERENCES -- `milestone_ref` is not SCD2, so
-- its `id` is table-wide unique, the same reasoning every other
-- `milestone_id` foreign key in this schema already relies on
-- (`entity_milestone`, `milestone_deferral`). Whether the referenced row
-- is a milestone or a milepebble is validated in the store layer
-- (RecordTransition), not by a DB CHECK here, mirroring how `kind`
-- widening itself has always been enforced at the milestone_ref level.
--
-- `status` is CHECK-constrained to FR8's seven-value set (FR9 uses the
-- identical set for a milepebble) -- rejected at the DB, not only by Go
-- validation, so a future write path cannot silently persist an eighth
-- value.
--
-- `created_by_acting_*`/`created_by_on_behalf_of_*` are NOT NULL (NFR4,
-- LB4) -- unlike `milestone_ref`'s own nullable subject-pair columns
-- (migration 010's LB4 note, which exists only because the importer's
-- pre-existing GetOrCreateRef path has no session-attributable actor),
-- every write path onto this brand-new table is the new RecordTransition
-- method, which always has a real caller session -- same posture as
-- `milestone_deferral`'s mandatory columns (migration 010).
CREATE TABLE milestone_status_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    milestone_id                 UUID        NOT NULL REFERENCES milestone_ref(id),
    status                       TEXT        NOT NULL CHECK (status IN (
                                     'not started',
                                     'in design',
                                     'planned',
                                     'in progress',
                                     'shipped',
                                     'partially complete',
                                     'abandoned'
                                 )),
    note                         TEXT        NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ordered by (milestone_id, created_at DESC) -- both CurrentStatus's
-- latest-row lookup and ListTransitions' chronological read (reversed)
-- hit this index; CurrentStatuses' DISTINCT ON / window-function form
-- over a set of ids scans it the same way.
CREATE INDEX milestone_status_event_milestone_created_idx ON milestone_status_event(milestone_id, created_at DESC);
