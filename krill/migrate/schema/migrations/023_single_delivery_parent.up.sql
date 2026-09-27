-- 023_single_delivery_parent: drop C6's duplicate Delivers association with
-- M7, so one entity has exactly one milestone-level delivery parent
-- (issue #3094, NFR c0629588, feature C36).
--
-- ============================================================================
-- Why this migration exists
-- ============================================================================
-- LB6 says an entity is delivered by at most one milestone. The importer
-- (migration 004's `entity_milestone`, migration 010's `relation`
-- discriminator) can write a Delivers row for the same entity against two
-- different milestones, and on krill's own roadmap it did exactly that once:
-- C6 was a Delivers association of BOTH M1 and M7. C6 is the only
-- capability in the product claimed twice.
--
-- M1's association STANDS and M7's is the wrong one. Three independent
-- records agree: C6 is the only capability double-claimed; LB3's note calls
-- C5/C6 "M1's own"; and M1's own cut note says "C6 cannot move in either
-- position".
--
-- This is a ONE-OFF DATA FIX and nothing else. It is not a general removal
-- verb, not a repair/violation distinction on the competing-delivery guard,
-- and not a change to any write path's behaviour: `add_delivers` and the
-- importer already refuse the duplicate state today, so nothing needs
-- teaching here.
--
-- `move_delivery_scope` is NOT the route to this fix, and its refusal is
-- correct rather than a bug to work around. C6 is recorded as shipped in
-- BOTH containers, and MoveScope refuses any entity shipped in its `from`
-- container with ErrEntityShipped. That is why the repair has to be a
-- migration writing the database directly, exactly as this file does.
--
-- ============================================================================
-- Exactly one row is deleted
-- ============================================================================
-- The DELETE below names its target three ways, all of which must agree
-- before a single row is touched:
--
--   * `relation = 'delivers'` -- migration 010's discriminator. A
--     must_not_foreclose association is a different statement about a
--     milestone entirely and is never in scope for this fix.
--   * the entity is the CURRENT revision of the Feature whose
--     `display_number` is 6 (migration 017's per-product numbering), in
--     the product that owns both an M1 and an M7.
--   * M1 already delivers that same Feature. This is the guard that makes
--     the migration provably one-sided: if the (C6, M1) row is not there,
--     there is no duplicate to repair and nothing is deleted at all. The
--     M1 row is read but never itself a DELETE target -- no clause names
--     it -- so it survives this migration by construction, not by
--     convention.
--
-- Resolving C6 and M7 by `display_number`/`name` rather than by a
-- hard-coded UUID pair is deliberate. The ids are instance data, and a
-- migration that carries them is a migration that can only ever be right
-- once, on one database; resolving by the citations the roadmap actually
-- renders is both self-documenting and testable against a fixture.
--
-- `valid_to IS NULL` appears on every lookup because migration 020 made
-- `milestone_ref` SCD2 alongside the spec-axis tables: a superseded
-- revision keeps its `id` and its `name`, so an unfiltered name match
-- could resolve the row twice. `feature` and `feature_set` have been SCD2
-- since migration 002 and need the same filter for the same reason.
--
-- ============================================================================
-- Idempotence
-- ============================================================================
-- A second run deletes nothing: the (C6, M7) row is gone, the (C6, M1) row
-- is still present, and the WHERE clause's `em.milestone_id = m7.id` no
-- longer matches any surviving row. Nothing here is guarded by a
-- precondition that a re-run could fail, and nothing writes -- so this is
-- a no-op on a database where the row is already absent, including a
-- database where M7 never delivered C6 at all.
--
-- ============================================================================
-- What is deliberately NOT touched
-- ============================================================================
-- Every `delivery_shipment` row on BOTH sides is left intact. That table
-- has no DELETE path anywhere in krill/store, and that structural absence
-- -- not a rule anyone has to remember -- is what makes "abandoning never
-- discards or alters the record of what it already shipped" true. The
-- retained (C6, M7) shipment row is the more accurate history: M7's design
-- session did mark C6 shipped.
--
-- Dropping the association alone is sufficient for the read surface.
-- `deliveryBreakdown` partitions on the container's own Delivers set and
-- consults `delivery_shipment` only for ids already inside it, so C6
-- leaves M7's shipped and unshipped sets with an identical read surface --
-- there is no orphaned shipment row for it to surface.
--
-- M7's `milestone_status` is also left alone, and that is a decision, not
-- an oversight. M7 genuinely shipped with C6 in its deliverable set, so
-- that stays true in the revision history; deleting the current-scope row
-- corrects the present and does not falsify the past. Closing M7's status
-- is a separate act, owned by whoever owns M7 -- approving a data fix on
-- one milestone is not approving another milestone's close.
--
-- ONE CONSEQUENCE, recorded so a future reader does not file it as a new
-- bug: after this migration, `move_delivery_scope` of C6 out of M7 fails
-- with ErrEntityNotInContainer rather than the more misleading
-- ErrEntityShipped, because MoveScope's validation pass checks the
-- container's Delivers association before it consults `delivery_shipment`.
-- Expected, not a regression.

DELETE FROM entity_milestone AS em
USING feature AS f
JOIN feature_set AS fs
       ON fs.id       = f.feature_set_id
      AND fs.valid_to IS NULL
JOIN milestone_ref AS m7
       ON m7.product_id = fs.product_id
      AND m7.scope_id   = fs.scope_id
      AND m7.name       = 'M7'
      AND m7.kind       = 'milestone'
      AND m7.valid_to  IS NULL
JOIN milestone_ref AS m1
       ON m1.product_id = fs.product_id
      AND m1.scope_id   = fs.scope_id
      AND m1.name       = 'M1'
      AND m1.kind       = 'milestone'
      AND m1.valid_to  IS NULL
WHERE f.valid_to      IS NULL
  AND f.display_number = 6
  AND f.scope_id       = fs.scope_id
  AND em.scope_id      = f.scope_id
  AND em.entity_id     = f.id
  AND em.milestone_id  = m7.id
  AND em.relation      = 'delivers'
  -- The retained half of the duplicate. Absent means there is nothing to
  -- repair here, so the migration deletes nothing rather than guessing.
  AND EXISTS (
        SELECT 1
        FROM entity_milestone AS keep
        WHERE keep.entity_id    = f.id
          AND keep.milestone_id = m1.id
          AND keep.relation     = 'delivers'
      );
