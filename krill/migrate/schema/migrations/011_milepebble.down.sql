-- Down-migrating with milepebble rows present drops them along with the
-- columns/indexes/constraints this migration added -- a milepebble has no
-- meaning once `parent_milestone_id` is gone, so there is no "downgrade a
-- milepebble to a bare milestone" behavior to preserve. Any
-- `entity_milestone` rows associating a feature/requirement to a deleted
-- milepebble are left in place (entity_milestone.milestone_id is not
-- cleaned up here) -- the same "down-migrations don't backfill data"
-- posture every other migration in this package takes; a schema that has
-- gone this far forward is not expected to cleanly reverse in production.
DELETE FROM milestone_ref WHERE kind = 'milepebble';

DROP INDEX IF EXISTS milestone_ref_parent_milestone_idx;
DROP INDEX IF EXISTS milestone_ref_milepebble_parent_name_idx;
DROP INDEX IF EXISTS milestone_ref_scope_product_name_idx;

CREATE UNIQUE INDEX milestone_ref_scope_product_name_idx ON milestone_ref(scope_id, product_id, name);

ALTER TABLE milestone_ref
    DROP CONSTRAINT IF EXISTS milestone_ref_milepebble_has_parent_check,
    DROP COLUMN IF EXISTS parent_milestone_id,
    DROP CONSTRAINT milestone_ref_kind_check,
    ADD CONSTRAINT milestone_ref_kind_check CHECK (kind IN ('milestone'));
