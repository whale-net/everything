-- Down-migrating with a backlog row present drops it along with the
-- widened kind/constraint this migration added -- matching migration
-- 011's own down posture (a milepebble has no meaning once its schema
-- support is gone; neither does a backlog bucket). Any `entity_milestone`
-- rows still associating an entity to a deleted backlog row are left in
-- place, same "down-migrations don't backfill data" posture every
-- migration in this package takes.
DELETE FROM milestone_ref WHERE kind = 'backlog';

DROP INDEX IF EXISTS milestone_ref_backlog_product_idx;

ALTER TABLE milestone_ref
    DROP CONSTRAINT milestone_ref_kind_parent_check,
    ADD CONSTRAINT milestone_ref_milepebble_has_parent_check
        CHECK ((kind = 'milepebble') = (parent_milestone_id IS NOT NULL));

ALTER TABLE milestone_ref
    DROP CONSTRAINT milestone_ref_kind_check,
    ADD CONSTRAINT milestone_ref_kind_check CHECK (kind IN ('milestone', 'milepebble'));
