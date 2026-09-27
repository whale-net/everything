-- 023_single_delivery_parent down: restore the (C6, M7) Delivers
-- association ONLY where restoring it cannot recreate the duplicate this
-- migration exists to remove.
--
-- The prior state was, by definition, a state in which C6 was delivered by
-- M1 and M7 at once -- the very thing LB6 forbids and the thing every
-- current write path already refuses to create. So this is not an
-- unconditional re-INSERT: the association is written back only when M1
-- does NOT already deliver C6, which is exactly the case where the row
-- that was removed was not part of a duplicate to begin with (a C6 that had
-- already been re-cut away from M7, or a database that never had the
-- duplicate at all).
--
-- In the corrected world -- M1 delivering C6, M7 not -- the EXISTS below
-- is false, so this down is a no-op. That is deliberate and is the whole
-- point: rolling this migration back must not put the product back into
-- the state it was migrated out of.
--
-- Idempotent: the INSERT carries `ON CONFLICT DO NOTHING` against
-- migration 010's `entity_milestone_entity_milestone_idx` unique index on
-- (entity_id, milestone_id, relation), so a second run writes nothing
-- rather than failing. Every lookup is filtered to current revisions for
-- the same reason the up migration's are -- migration 020 made
-- `milestone_ref` SCD2, and migration 002 made `feature`/`feature_set`
-- SCD2 before it.
--
-- `delivery_shipment` rows are NOT restored, matching every other
-- down-migration in this package's posture that down-migrations do not
-- backfill data. There is nothing to restore: this migration deleted none.

INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
SELECT f.scope_id, f.id, m7.id, 'delivers'
FROM feature AS f
JOIN feature_set AS fs
       ON fs.id       = f.feature_set_id
      AND fs.valid_to IS NULL
JOIN milestone_ref AS m7
       ON m7.product_id = fs.product_id
      AND m7.scope_id   = fs.scope_id
      AND m7.name       = 'M7'
      AND m7.kind       = 'milestone'
      AND m7.valid_to  IS NULL
WHERE f.valid_to       IS NULL
  AND f.display_number = 6
  AND f.scope_id       = fs.scope_id
  AND NOT EXISTS (
        SELECT 1
        FROM entity_milestone AS other
        WHERE other.entity_id    = f.id
          AND other.relation     = 'delivers'
          AND other.milestone_id IN (
                SELECT m1.id
                FROM milestone_ref AS m1
                WHERE m1.product_id = fs.product_id
                  AND m1.scope_id   = fs.scope_id
                  AND m1.name       = 'M1'
                  AND m1.kind       = 'milestone'
                  AND m1.valid_to  IS NULL
              )
      )
ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING;
