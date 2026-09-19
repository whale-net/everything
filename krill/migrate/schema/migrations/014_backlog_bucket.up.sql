-- 014_backlog_bucket: the backlog bucket (issue #2687, FR5, C13) --
-- the destination a Requirement Contributor re-cuts not-yet-shipped scope
-- into when it leaves a milestone or milepebble without landing in
-- another one. This is FR6's explicit wording, carried out here because
-- FR5 (this issue) is the first place a move destination is needed: the
-- bucket is a `milestone_ref` row, `kind='backlog'`, via the exact same
-- `milestone_ref`/`entity_milestone` mechanism migration 010 (#2683) and
-- 011 (#2684) already established for a milestone and a milepebble --
-- never a parallel table, and never a new association mechanism.
--
-- ============================================================================
-- The backlog bucket is NOT the product's `Later` capability bucket
-- ============================================================================
-- `krill/product/02-capability-map.md`'s `Later` heading is a spec-axis
-- statement -- a capability-map entry (`Cn`) the product may someday
-- build, never committed to any milestone at all. The backlog bucket
-- introduced here is a delivery-axis home for scope that WAS committed
-- (it had a `entity_milestone` Delivers association to some milestone or
-- milepebble) and was then un-committed by a re-cut (FR5) or an abandon
-- (FR6). An item can move from a milestone to backlog; nothing ever moves
-- between backlog and `Later` -- they answer different questions ("will
-- we ever build this" vs. "is this currently slated for a delivery
-- date") and this migration keeps them on entirely separate mechanisms
-- (`milestone_ref` vs. the capability-map markdown FR5-9's slice reads
-- serve, never the reverse).
--
-- ============================================================================
-- LB3 -- still plain mutable, not SCD2 (see migrations 004/010/011's own notes)
-- ============================================================================
-- A backlog bucket row's existence is a fact set once (there either is or
-- is not a backlog bucket for a given (scope, product)), not a value with
-- history worth versioning -- no `valid_from`/`valid_to` pair, matching
-- every other `milestone_ref` column.
ALTER TABLE milestone_ref
    DROP CONSTRAINT milestone_ref_kind_check,
    ADD CONSTRAINT milestone_ref_kind_check CHECK (kind IN ('milestone', 'milepebble', 'backlog'));

-- Relax migration 011's `kind='milepebble' <=> parent_milestone_id IS NOT
-- NULL` pairing check: a `kind='backlog'` row has no parent milestone
-- (the bucket sits directly under a product, the same shape a milestone
-- does), so the two-way `<=>` no longer holds for every kind. The
-- replacement keeps both original directions migration 011 enforced
-- (a milepebble always has a parent; a milestone never does) and adds a
-- third: a backlog row never has a parent either.
ALTER TABLE milestone_ref
    DROP CONSTRAINT milestone_ref_milepebble_has_parent_check,
    ADD CONSTRAINT milestone_ref_kind_parent_check CHECK (
        (kind = 'milepebble' AND parent_milestone_id IS NOT NULL)
        OR (kind IN ('milestone', 'backlog') AND parent_milestone_id IS NULL)
    );

-- ============================================================================
-- milestone_ref_backlog_product_idx -- at most one backlog row per (scope, product)
-- ============================================================================
-- GetOrCreateBacklog (krill/store, Implementation phase) must resolve to
-- exactly one row per (scope_id, product_id) the same way
-- MilestoneStore.GetOrCreateRef relies on a unique index rather than an
-- application-level lock -- a partial unique index, scoped to
-- `kind = 'backlog'`, is this migration's DB-enforced half of that
-- idempotency guarantee. `parent_milestone_id` is always NULL for a
-- backlog row (the CHECK above), so it plays no part in this index,
-- unlike the milepebble-parent-scoped index migration 011 added.
CREATE UNIQUE INDEX milestone_ref_backlog_product_idx
    ON milestone_ref(scope_id, product_id)
    WHERE kind = 'backlog';

-- NOTE for the renderer (krill/render, issue #2495/#2683): `renderMilestones`
-- already filters `ListMilestoneRefs`' results to `kind = 'milestone'`
-- before rendering `product/03-roadmap.md` (see render.go's own comment,
-- added by migration 010) -- that filter, unchanged by this migration,
-- is what keeps a `kind='backlog'` row out of rendered roadmap output.
