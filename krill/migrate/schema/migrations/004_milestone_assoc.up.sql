-- 004_milestone_assoc: bare milestone references and the delivery-axis
-- association (issue #2492, FR17, LB6). This is the *only* delivery-axis
-- surface M1 ships -- see PRODUCT.md's LB6 and product/03-roadmap.md's "The
-- LB6 note" for the milestone's own framing: M1 can hold and render
-- milestones that arrive inside an imported document; it authors none.
--
-- ============================================================================
-- LB6 -- an association, never a second parent
-- ============================================================================
-- `milestone_ref` holds only the bare `M<n>` identifier a source document
-- names -- no status column, no milepebble table, no authoring fields. Those
-- are M3's (C13, C28); adding any of them here is exactly the trap LB6's own
-- note warns against. `entity_milestone` is the association, keyed
-- `(entity_id, milestone_id)` -- never a `milestone_id` column added to
-- `feature`, `requirement`, or `load_bearing_decision`. A `Must not
-- foreclose: LB1, LB4` line in an imported roadmap becomes two rows here,
-- one per cited entity, against the milestone whose own heading the line
-- appeared under -- never prose kept only in a rendered doc.
--
-- ============================================================================
-- LB1 -- scope, LB3 -- mutation shape (both non-SCD2)
-- ============================================================================
-- Both tables carry `scope_id NOT NULL REFERENCES scope(id)` (LB1). Neither
-- is SCD2 (AGENTS.md "SCD2"): a milestone reference is a bare fact ("this
-- product's roadmap names an M3") with no history worth versioning, and an
-- association row is either present or absent, never superseded in place --
-- so both are plain mutable rows with no `valid_from`/`valid_to` pair, the
-- same boundary call `scope` (001_scope.up.sql) and `krill_session`
-- (003_session.up.sql) already made for their own non-SCD2 reasons.
--
-- ============================================================================
-- milestone_ref -- delivery axis, not SCD2 (LB3)
-- ============================================================================
-- `product_id` is the parent's immutable `id` (migration 002's LB2
-- parentage note: not a DB-enforced REFERENCES, because `product.id` is not
-- unique table-wide -- `krill/store`'s `currentRowExists` enforces it at
-- write time, same as every other child table). `name` is the bare `M<n>`
-- identifier as the source document spells it (e.g. "M1") -- never a parsed
-- integer column; two different products under the same scope may each
-- have their own "M1", so the natural key is scope+product+name, not name
-- alone.
CREATE TABLE milestone_ref (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id    UUID        NOT NULL REFERENCES scope(id),
    product_id  UUID        NOT NULL, -- parent's immutable `id`; see migration 002's LB2 parentage note
    name        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The importer's "creating it on first reference if absent for that
-- Product" idempotency guarantee: a second import of the same document
-- resolves to this same row rather than inserting a duplicate.
CREATE UNIQUE INDEX milestone_ref_scope_product_name_idx ON milestone_ref(scope_id, product_id, name);

-- ============================================================================
-- entity_milestone -- delivery axis association, not SCD2 (LB3)
-- ============================================================================
-- `entity_id` is a spec entity's immutable `id` (a `feature.id` for a `Cn`
-- citation, a `load_bearing_decision.id` for an `LBn` citation -- never a
-- `revision_id`, for the same reason migration 002's child tables key off
-- the immutable id rather than a specific revision). `milestone_id` IS a
-- real DB-enforced REFERENCES here, unlike every parent link in migration
-- 002: `milestone_ref` is not SCD2, so its `id` genuinely is unique
-- table-wide, and a real FK can target it.
CREATE TABLE entity_milestone (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id      UUID        NOT NULL REFERENCES scope(id),
    entity_id     UUID        NOT NULL,
    milestone_id  UUID        NOT NULL REFERENCES milestone_ref(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Importing the same document twice must not duplicate an association row
-- either -- the importer upserts against this on (entity_id, milestone_id).
CREATE UNIQUE INDEX entity_milestone_entity_milestone_idx ON entity_milestone(entity_id, milestone_id);
CREATE INDEX entity_milestone_milestone_idx ON entity_milestone(milestone_id);
