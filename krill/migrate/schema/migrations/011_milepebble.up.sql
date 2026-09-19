-- 011_milepebble: milepebbles -- sub-milestone containers a milestone is
-- cut into (issue #2684, FR3, FR4, C13) -- widening `milestone_ref.kind`
-- (migration 010, issue #2683) rather than adding a parallel table or a
-- `milepebble_id` column anywhere (FR3/LB6). A milepebble IS a
-- `milestone_ref` row, `kind='milepebble'`, with a `parent_milestone_id`
-- naming the one milestone it is cut from -- its delivered scope is
-- recorded the same `entity_milestone` way migration 004 already
-- established (LB6), never a second association mechanism.
--
-- ============================================================================
-- LB3 -- milestone_ref's widened kind + new parent_milestone_id: plain mutable, not SCD2
-- ============================================================================
-- Same boundary call as every other milestone_ref column (see
-- 004_milestone_assoc.up.sql's and 010_milestone_authoring.up.sql's own LB3
-- notes): a milepebble's parent assignment is a fact set once at creation,
-- not a value with history worth versioning here -- no `valid_from`/
-- `valid_to` pair.
--
-- `parent_milestone_id` IS a real DB-enforced REFERENCES back onto
-- `milestone_ref(id)` -- `milestone_ref` is not SCD2, so its `id` is
-- table-wide unique, the same reasoning `entity_milestone.milestone_id`
-- and `milestone_deferral.milestone_id` already rely on. It is NULL for
-- every `kind='milestone'` row (a milestone has no parent milestone) and
-- NOT NULL for every `kind='milepebble'` row (FR3: "a milepebble is
-- scoped to exactly one milestone") -- enforced by the CHECK below, not
-- just documented convention, so a future write path cannot silently
-- create an orphaned or doubly-parented milepebble.
ALTER TABLE milestone_ref
    DROP CONSTRAINT milestone_ref_kind_check,
    ADD CONSTRAINT milestone_ref_kind_check CHECK (kind IN ('milestone', 'milepebble')),
    ADD COLUMN parent_milestone_id UUID NULL REFERENCES milestone_ref(id),
    ADD CONSTRAINT milestone_ref_milepebble_has_parent_check
        CHECK ((kind = 'milepebble') = (parent_milestone_id IS NOT NULL));

-- ============================================================================
-- milestone_ref_scope_product_name_idx -- re-scoped per parent, not dropped
-- ============================================================================
-- The original index (migration 004) enforced one name per (scope,
-- product) with no notion of a parent -- correct for milestones (still
-- one namespace per product), but wrong once milepebbles exist: two
-- milepebbles cut from *different* parent milestones may legitimately
-- want the same short name (e.g. both call theirs "cut 1"), and Postgres
-- unique indexes treat every NULL as distinct from every other NULL, so a
-- single `(scope_id, product_id, parent_milestone_id, name)` index would
-- silently stop deduplicating milestones themselves (parent_milestone_id
-- is NULL on all of them). Two partial indexes instead, one per kind,
-- preserve the original milestone-level uniqueness unchanged and add the
-- new parent-scoped uniqueness for milepebbles:
DROP INDEX milestone_ref_scope_product_name_idx;

CREATE UNIQUE INDEX milestone_ref_scope_product_name_idx
    ON milestone_ref(scope_id, product_id, name)
    WHERE parent_milestone_id IS NULL;

CREATE UNIQUE INDEX milestone_ref_milepebble_parent_name_idx
    ON milestone_ref(scope_id, product_id, parent_milestone_id, name)
    WHERE parent_milestone_id IS NOT NULL;

CREATE INDEX milestone_ref_parent_milestone_idx ON milestone_ref(parent_milestone_id);
