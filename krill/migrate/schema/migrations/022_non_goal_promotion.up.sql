-- 022_non_goal_promotion: the PROMOTE register (FR d0021a0f), and the
-- `outcome` column that tells a RETIRE apart from a mistaken-create void
-- in the tombstone register migration 021 added.
--
-- ============================================================================
-- Why this migration exists
-- ============================================================================
-- Resolving a `deferred` Non-Goal has exactly two outcomes, and they land
-- in two different registers because they leave two different shapes
-- behind:
--
--   RETIRE  closes the current row and opens no successor. That is a
--           tombstone -- byte-for-byte the shape a void leaves (FR
--           d38d726e) -- so it is recorded in `void_event` and reuses that
--           path rather than growing a second tombstone register. The one
--           thing a reader needs beyond a void is WHICH closed it, and
--           that is the `outcome` column below.
--
--   PROMOTE closes the current row AND opens a successor under the same
--           surrogate id, re-kinded to `permanent`. The closed row is
--           indistinguishable from an amend's closed row, and the id still
--           has a current revision -- so a `void_event` row would be a
--           false tombstone, and its `(scope_id, entity_kind, entity_id)`
--           unique index would permanently block a later genuine void of
--           that same Non-Goal. This table is therefore its own register
--             and not a second column on the other one.
--
-- ============================================================================
-- LB2 -- a promotion mints no id and frees no name
-- ============================================================================
-- The successor is inserted under the SAME `id` as the closed revision, so
-- a citation already rendered for the deferred Non-Goal still resolves, and
-- nothing can slip through the re-kind under a new name. The row's unique
-- name is carried forward unchanged and is never freed, because the row is
-- never tombstoned: `non_goal_scope_product_name_current_idx` is partial on
-- `valid_to IS NULL`, and a promoted row is always current.
--
-- A Non-Goal carries no stored `display_number` (migration 002's LB2 note
-- -- `NG`-style citations are derived at render time from `position`), so
-- there is no number to retire here and no counterpart to
-- `void_event.retired_display_number`. The close-without-successor case is
-- the one that retires a number, and it lives in migration 021.
--
-- ============================================================================
-- LB3 -- append-only, not SCD2
-- ============================================================================
-- A non_goal_promotion row is a fact about one moment ("this Non-Goal was
-- promoted, by this actor, at this time"), so it is append-only with no
-- `valid_from`/`valid_to` pair -- the same boundary call migration 021
-- draws for `void_event`, and for migrations 010/012/013 before it. It is
-- not a spec-axis table: it never takes part in amend or supersession.
--
-- The unique index below makes "a Non-Goal is promoted at most once, ever"
-- a database-enforced invariant rather than a store-layer convention. It
-- holds because promotion's own terminal state forbids a second one: the
-- successor is `permanent`, and only a `deferred` Non-Goal is resolvable
-- (FR d0021a0f). The store refuses a permanent target before it writes
-- anything; this index is what would catch it if that check were ever
-- dropped, which is the point of stating it here.
--
-- `product_id` is denormalised from the promoted row rather than joined,
-- for the same reason `void_event` carries one: the audit read is
-- scope-keyed and per-product, and a Non-Goal is directly product-level
-- (migration 002), so the store resolves it in the same transaction.

CREATE TABLE non_goal_promotion (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    non_goal_id                  UUID        NOT NULL, -- the promoted row's immutable `id` (LB2), NOT a `revision_id`
    product_id                   UUID        NOT NULL, -- the product the promoted Non-Goal belongs to
    from_kind                    TEXT        NOT NULL CHECK (from_kind = 'deferred'),
    to_kind                      TEXT        NOT NULL CHECK (to_kind = 'permanent'),
    reason                       TEXT        NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A Non-Goal is promoted at most once, ever (see the LB3 section above).
CREATE UNIQUE INDEX non_goal_promotion_scope_non_goal_idx
    ON non_goal_promotion(scope_id, non_goal_id);

-- Serves the audit read -- every promotion in this scope, optionally
-- narrowed to one product -- which is the only way a caller reaches the
-- fact that a now-`permanent` Non-Goal was once `deferred`: the SCD2 row
-- itself says only that kind changed, not that it changed by promotion
-- rather than by a create that happened to reuse the id.
CREATE INDEX non_goal_promotion_scope_product_idx
    ON non_goal_promotion(scope_id, product_id);

-- ============================================================================
-- Telling a RETIRE apart from a mistaken-create void
-- ============================================================================
-- FR d0021a0f's RETIRE is a close-WITHOUT-successor on a `deferred`
-- Non-Goal; FR d38d726e's void is the same close on any kind, of either
-- kind. Both leave a tombstone, both are recorded in `void_event`, and
-- nothing in the closed SCD2 row distinguishes them -- so an auditor
-- reading history would otherwise see a tombstone and have to guess
-- whether the Non-Goal was retracted or had simply been settled.
--
-- The default is `'void'` and every row written before this migration is a
-- genuine void, so the backfill needs no data pass: an existing row means
-- what it already meant. A RETIRE names itself explicitly. Both values are
-- CHECK-constrained, so a new outcome cannot be introduced without a
-- migration -- the same discipline `entity_kind` and `requirement.kind`
-- already follow.
--
-- Only the two close-without-successor outcomes can appear here, so the
-- column is not free text: a PROMOTE never reaches this table (see the
-- section above) and an AMEND is not a close-without-successor at all.
ALTER TABLE void_event
    ADD COLUMN outcome TEXT NOT NULL DEFAULT 'void'
        CHECK (outcome IN ('void', 'retire'));
