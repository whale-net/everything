-- 020_milestone_scd2: make `milestone_ref` SCD2 (LB2/LB3), so a
-- milestone's authoring fields (name, outcome) are amendable by
-- supersession under one immutable surrogate id -- the same primitive
-- every other spec-axis entity kind has (FR 8b2e87d1, FR 39373553).
--
-- ============================================================================
-- Why this migration exists
-- ============================================================================
-- Migrations 004/010/011/014 all recorded the same LB3 boundary call: a
-- milestone is a bare fact ("this product's roadmap names an M3"), not a
-- value that changes over time, so `milestone_ref` shipped with no
-- `valid_from`/`valid_to` pair while every spec-axis table around it
-- (migration 002) had one. That call is what left Milestone the one kind
-- with no amend path: a supersession needs somewhere to put the prior
-- revision's `valid_to`.
--
-- The milestone's *delivery* axis stays exactly as it was. Status
-- transitions (`milestone_status_event`, migration 012), shipments
-- (`delivery_shipment`, migration 013), the Delivers/Must-not-foreclose
-- associations (`entity_milestone`, migrations 004/010), and deferrals
-- (`milestone_deferral`, migration 010) are append-only facts keyed on the
-- immutable `id` -- an amend reuses that same `id`, so none of them is
-- read, written, or even reachable by a supersession. That is the whole
-- point of FR 39373553: authoring content is amendable, delivery history
-- is not touched by amending.
--
-- ============================================================================
-- LB2 -- identity: revision_id becomes the row key, `id` stops being the PK
-- ============================================================================
-- `id` remains the immutable surrogate id every revision of one logical
-- milestone shares, and every child table keeps keying on it unchanged.
-- What changes is that `id` can no longer be the PRIMARY KEY, because two
-- revisions of one milestone now legitimately carry the same value --
-- exactly the reason migration 002 gave for `product`/`feature_set`/
-- `feature`/`requirement`/`load_bearing_decision`/`persona`/`non_goal`
-- never declaring `id` UNIQUE. `revision_id` takes over as the per-row
-- SCD2 key, matching those seven tables' shape column-for-column.
--
-- Every pre-existing row becomes its own first revision: `valid_to` lands
-- NULL (current) and `valid_from` NOW(), so this migration changes no
-- rendered roadmap line and retires no id.
--
-- ============================================================================
-- The five child FKs become plain UUID columns -- the LB2 parentage rule
-- ============================================================================
-- Migrations 004/010/011/012/013/015 each state in their comments that
-- `milestone_id UUID REFERENCES milestone_ref(id)` "is a real
-- DB-enforced REFERENCES -- `milestone_ref` is not SCD2, so its `id` is
-- table-wide unique". That premise is exactly what this migration
-- removes: with `id` no longer table-wide unique, Postgres can no longer
-- target a FK at it. Rather than invent a second identity table to keep
-- the constraint alive, this migration applies the boundary migration 002
-- already drew for every other spec-axis parent link -- a plain UUID
-- column holding the parent's immutable `id`, with parent existence
-- validated by `krill/store` inside the writing transaction
-- (`currentRowExists`/`errParentNotFound`, errors.go). That migration's
-- own comment anticipates this exact situation: "documented here so a
-- future migration doesn't 'fix' this by attempting a REFERENCES clause
-- that cannot express the invariant it would appear to add."
--
-- The child columns keep their names, types, and NOT NULL constraints;
-- only the referential action is dropped. `milestone_ref`'s own
-- self-referencing `parent_milestone_id` (migration 011) is treated the
-- same way, for the same reason.
--
-- ============================================================================
-- Indexes: every uniqueness becomes "among current rows"
-- ============================================================================
-- Each existing unique index gains `AND valid_to IS NULL`, so a superseded
-- revision's name never blocks its own successor (an amend that keeps the
-- same name must succeed) while a name held by a *different* live
-- sibling still collides -- which is the sibling-uniqueness rule amend
-- validates against, identical to create's.
ALTER TABLE milestone_ref
    ADD COLUMN revision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN valid_to    TIMESTAMPTZ NULL;

ALTER TABLE entity_milestone         DROP CONSTRAINT entity_milestone_milestone_id_fkey;
ALTER TABLE milestone_deferral       DROP CONSTRAINT milestone_deferral_milestone_id_fkey;
ALTER TABLE milestone_status_event   DROP CONSTRAINT milestone_status_event_milestone_id_fkey;
ALTER TABLE delivery_shipment        DROP CONSTRAINT delivery_shipment_milestone_id_fkey;
ALTER TABLE task                     DROP CONSTRAINT task_milestone_id_fkey;
ALTER TABLE milestone_ref            DROP CONSTRAINT milestone_ref_parent_milestone_id_fkey;

ALTER TABLE milestone_ref DROP CONSTRAINT milestone_ref_pkey;
ALTER TABLE milestone_ref ADD PRIMARY KEY (revision_id);

CREATE UNIQUE INDEX milestone_ref_current_id_idx ON milestone_ref(id) WHERE valid_to IS NULL;

DROP INDEX milestone_ref_scope_product_name_idx;
CREATE UNIQUE INDEX milestone_ref_scope_product_name_idx
    ON milestone_ref(scope_id, product_id, name)
    WHERE parent_milestone_id IS NULL AND valid_to IS NULL;

DROP INDEX milestone_ref_milepebble_parent_name_idx;
CREATE UNIQUE INDEX milestone_ref_milepebble_parent_name_idx
    ON milestone_ref(scope_id, product_id, parent_milestone_id, name)
    WHERE parent_milestone_id IS NOT NULL AND valid_to IS NULL;

DROP INDEX milestone_ref_backlog_product_idx;
CREATE UNIQUE INDEX milestone_ref_backlog_product_idx
    ON milestone_ref(scope_id, product_id)
    WHERE kind = 'backlog' AND valid_to IS NULL;

DROP INDEX milestone_ref_parent_milestone_idx;
CREATE INDEX milestone_ref_parent_milestone_idx
    ON milestone_ref(parent_milestone_id)
    WHERE valid_to IS NULL;
