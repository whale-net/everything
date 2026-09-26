-- 021_void_event: the void tombstone register (FR d38d726e), and the
-- non-partial indexes that make a retired `display_number` countable.
--
-- ============================================================================
-- Why this migration exists
-- ============================================================================
-- Void is an SCD2 close-WITHOUT-successor (LB3): the current revision's
-- `valid_to` is set and no successor revision is inserted. That frees the
-- row's unique name -- every name index in migration 002 (and 020) is
-- partial on `valid_to IS NULL`, so a closed row stops occupying its name
-- and a later create may reuse it -- while leaving the row itself in place,
-- still carrying its original `id` and its original `display_number`.
--
-- The closed SCD2 row is not by itself a sufficient record of *why* it is
-- closed, because that is exactly the shape an amend's closed row has too:
-- an amend closes a row and opens a successor under the same id, so "this
-- id has a closed revision and no current revision" is what tells the two
-- apart, and nothing in the row records the actor or the intent. This
-- table records that explicitly, and does so uniformly across all seven
-- void-able spec-axis kinds rather than only for the two kinds the
-- history reads (FR11) happen to cover today.
--
-- ============================================================================
-- LB2 -- the retired display_number is never reissued
-- ============================================================================
-- `retired_display_number` records the number a voided row carried, for
-- the two kinds that have one at all (feature and load_bearing_decision;
-- migration 017). It is NULL for every other kind: `FRn`/`NFRn` and the
-- brief-document kinds have no stored display number -- `FR7`-style
-- citations are derived at render time from `position` (migration 002's
-- LB2 note), so there is no number here to retire.
--
-- The partial unique index below makes "a number is retired for exactly
-- one entity in one product, forever" a database-enforced invariant rather
-- than a store-layer convention: a second void cannot record a number
-- another entity already retired. This is what stops a `C7` citation
-- already rendered for a voided Feature from ever resolving onto a
-- different Feature.
--
-- The complementary half of the guarantee is that a CREATE can never be
-- *handed* a retired number in the first place. `nextDisplayNumber`
-- (store/position.go) counts every row the product has ever had, current
-- or closed, not just its current rows -- which is why this migration
-- replaces the partial `(feature_set_id) WHERE valid_to IS NULL` indexes
-- with non-partial ones below. No database constraint can express
-- "display_number is unique per product" on these two tables, because
-- neither carries a `product_id` column; the store is where that
-- invariant lives, exactly as the LB2 parentage rule already puts parent
-- existence in the store (migration 002's note above).
--
-- ============================================================================
-- LB3 -- append-only, not SCD2
-- ============================================================================
-- A void_event row is a fact about one moment ("this entity was voided,
-- by this actor, at this time"), so it is append-only with no
-- `valid_from`/`valid_to` pair -- the same boundary call migrations
-- 010/012/013 drew for the other delivery-axis registers. It is not a
-- spec-axis table and takes no part in amend or supersession: voiding
-- twice is impossible (the unique index below, plus the fact that a
-- second void finds no current row to close), and nothing amends a
-- tombstone.
--
-- `product_id` is NOT NULL for every kind rather than nullable: a
-- FeatureSet, Persona, or NonGoal carries it directly, a Product is its
-- own, and a Feature, LoadBearingDecision, or Requirement reaches it
-- through its `feature_set` ancestor. The store resolves it inside the
-- void transaction, so the number-retirement index is per product in the
-- same sense migration 017's numbering is.

CREATE TABLE void_event (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    entity_kind                  TEXT        NOT NULL CHECK (entity_kind IN (
                                    'product', 'feature_set', 'feature', 'requirement',
                                    'load_bearing_decision', 'persona', 'non_goal')),
    entity_id                    UUID        NOT NULL, -- the voided row's immutable `id` (LB2); never a `revision_id`
    product_id                   UUID        NOT NULL, -- the product whose numbering sequence this void touched
    retired_display_number       INT         NULL,    -- only feature/load_bearing_decision carry one (migration 017)
    reason                       TEXT        NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- An entity is voided at most once, ever: the second attempt finds no
-- current row to close and is refused before it can reach this index.
CREATE UNIQUE INDEX void_event_scope_kind_entity_idx
    ON void_event(scope_id, entity_kind, entity_id);

-- The number-retirement invariant (see the LB2 section above). Partial on
-- the non-NULL number, so the five kinds that have none neither collide
-- nor constrain each other.
CREATE UNIQUE INDEX void_event_scope_kind_product_number_idx
    ON void_event(scope_id, entity_kind, product_id, retired_display_number)
    WHERE retired_display_number IS NOT NULL;

-- Serves the audit read -- "every void in this scope", optionally
-- narrowed to one kind -- which is the only way a caller reaches a
-- tombstone; no current read sees one.
CREATE INDEX void_event_scope_kind_idx ON void_event(scope_id, entity_kind);

-- ============================================================================
-- Non-partial parent indexes: nextDisplayNumber counts closed rows too
-- ============================================================================
-- store/position.go's nextDisplayNumber must see every row a product has
-- ever had -- a voided row's number is retired precisely BECAUSE it is
-- still sitting in the table with `valid_to` set -- so the partial
-- `WHERE valid_to IS NULL` indexes migration 002 put on these two tables
-- cannot serve it: a closed row is not in either index's range at all.
-- These two are ADDED alongside migration 002's, not substituted for
-- them -- the partial ones stay, because they are exactly right for every
-- current-row lookup and for name uniqueness (a voided row must stop
-- blocking its freed name, which is precisely what a partial index on
-- `valid_to IS NULL` buys). `display_number` rides along so the
-- all-rows MAX is answerable from the new index alone.
--
-- Only the two display-number-bearing tables need this. The other five
-- have no stored number to retire, so their existing partial indexes are
-- already the whole story.
CREATE INDEX feature_feature_set_all_idx ON feature(feature_set_id, display_number);
CREATE INDEX load_bearing_decision_feature_set_all_idx
    ON load_bearing_decision(feature_set_id, display_number);
