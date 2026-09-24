-- 017_display_numbers: a stable, stored `display_number` on `feature` and
-- `load_bearing_decision` (issue #2969), reversing migration 002's LB2
-- stance ("no table has a `display_number`... column -- LB2's trap") for
-- exactly these two entity kinds. `krill/render`'s `numberByOrder`
-- previously recomputed every `Cn`/`LBn` citation from each entity's
-- *current position among siblings* on every render -- stable only as long
-- as nothing was ever appended out of order, reordered, or superseded.
-- Krill's own capability map already relies on out-of-order appends (C25-
-- C28) to keep earlier citations resolving, a convention the old
-- render-time computation could not actually honor. `Position` itself is
-- untouched by this migration and keeps its existing job (sibling display
-- order); `display_number` is a second, independent column that -- unlike
-- `position` -- is assigned once at creation (or, for an imported document,
-- taken verbatim from the source's own `Cn`/`LBn` token) and never
-- rewritten by a later insert or reorder.
--
-- Milestones (`Mn`) are not touched here -- `milestone_ref.name` is
-- already a literal stored string (LB6), never recomputed. Requirements
-- (`FRn`/`NFRn`) have no render path yet, so are out of this migration's
-- scope too (see issue #2969's own "Where it applies").
--
-- Scope of numbering: `display_number` is unique *per product*, not per
-- immediate FeatureSet -- `krill/render`'s `numberByOrder` numbered
-- `doc.Features`/`doc.Decisions` (the whole product's list, per
-- `ListFeaturesByProduct`/`ListDecisionsByProduct`, store/slice.go), not
-- one FeatureSet's own children, and the new column preserves that same
-- numbering scope.
--
-- Nullable-then-backfill-then-NOT-NULL: both columns land NULL so the
-- backfill below can compute today's already-rendered order before the
-- column is required, rather than needing every row's value up front.

ALTER TABLE feature ADD COLUMN display_number INT NULL;
ALTER TABLE load_bearing_decision ADD COLUMN display_number INT NULL;

-- Backfill: assign every current Feature/LoadBearingDecision the exact
-- number `numberByOrder` would already have rendered it as -- the same
-- (feature_set.position, feature_set.name, <table>.position, <table>.name)
-- order, partitioned per product so two different Products' numbering
-- never collide. This migration therefore changes no existing rendered
-- citation; it only stops future renders from being able to change one.
WITH numbered_feature AS (
    SELECT feature.revision_id,
           ROW_NUMBER() OVER (
               PARTITION BY feature_set.product_id
               ORDER BY feature_set.position, feature_set.name, feature.position, feature.name
           ) AS n
    FROM feature
    JOIN feature_set ON feature.feature_set_id = feature_set.id AND feature_set.valid_to IS NULL
    WHERE feature.valid_to IS NULL
)
UPDATE feature SET display_number = numbered_feature.n
FROM numbered_feature
WHERE feature.revision_id = numbered_feature.revision_id;

WITH numbered_decision AS (
    SELECT load_bearing_decision.revision_id,
           ROW_NUMBER() OVER (
               PARTITION BY feature_set.product_id
               ORDER BY feature_set.position, feature_set.name, load_bearing_decision.position, load_bearing_decision.name
           ) AS n
    FROM load_bearing_decision
    JOIN feature_set ON load_bearing_decision.feature_set_id = feature_set.id AND feature_set.valid_to IS NULL
    WHERE load_bearing_decision.valid_to IS NULL
)
UPDATE load_bearing_decision SET display_number = numbered_decision.n
FROM numbered_decision
WHERE load_bearing_decision.revision_id = numbered_decision.revision_id;

-- Propagate each LoadBearingDecision lineage's number to its own
-- historical (superseded) rows too, by matching the immutable `id` --
-- `AmendLoadBearingDecision` (store/amend.go) carries `display_number`
-- forward on supersession exactly like it already does `position`, so
-- every row of one logical decision's history shares one number.
-- `feature` has no amend/supersession path today, so every `feature` row
-- is already current -- no propagation step is needed for it.
UPDATE load_bearing_decision AS historical
SET display_number = current.display_number
FROM load_bearing_decision AS current
WHERE historical.id = current.id
  AND current.valid_to IS NULL
  AND historical.valid_to IS NOT NULL;

ALTER TABLE feature ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE load_bearing_decision ALTER COLUMN display_number SET NOT NULL;
