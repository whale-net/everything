-- 023_milestone_deferral_scd2: make `milestone_deferral` SCD2, so a
-- deferral's body and destination are correctable by supersession under
-- one immutable id -- the same primitive migration 020 gave
-- `milestone_ref` and migration 002 gave every spec-axis table.
--
-- ============================================================================
-- Why this migration exists: it reverses migration 010's own LB3 call
-- ============================================================================
-- 010_milestone_authoring.up.sql closed with an explicit boundary
-- decision, still quoted in the store's MilestoneDeferral doc comment:
--
--   "A deferral is a fact ... not a value that changes over time -- there
--   is no 'un-defer' or 'revise a deferral' operation in this issue's
--   scope, so this is a plain append-only table, no valid_from/valid_to
--   pair."
--
-- The premise was about the deferral's *existence*, and on that premise it
-- still holds: a deferral is never withdrawn, and no amend retires one.
-- What it got wrong was the consequence it drew from it -- that the
-- deferral's *body* was also immutable. A deferral's body cites concrete
-- capability numbers, and those numbers are exactly what LB2's identity
-- discipline makes immutable while letting their labels change (see
-- 017_display_numbers.up.sql, which retires a number forever rather than
-- reissuing it). So a milestone's authored deferral goes stale the moment
-- the product around it is renumbered, and today there is no way to correct
-- the text: the only available move is delete-and-recreate, which mints a
-- new id, loses the "deferred on <date>, by <subject>" provenance the
-- created_by_* columns exist to record (NFR4), and silently reorders the
-- item against its siblings because `position` is assigned per create.
--
-- That is a correction-surface gap, not a modelling preference: every
-- other correctable row in krill is SCD2 under LB3, and this was the one
-- authoring row that was not. This migration makes the table SCD2 and
-- krill/store's AmendDeferral supersedes a revision under the SAME
-- immutable id, so the evidence of the original deferral survives and the
-- rendered roadmap shows the amended text exactly once.
--
-- The scope of the reversal is deliberately narrow. It does not make a
-- deferral withdrawable: `valid_to` on a deferral row means only "this
-- revision's text was corrected", never "this item is no longer
-- deferred". There is still no un-defer verb, exactly as 010 said there
-- would not be, and `destination` remains NOT NULL (FR1) on every
-- revision.
--
-- ============================================================================
-- LB2 -- identity: revision_id becomes the row key, `id` stops being the PK
-- ============================================================================
-- The same move migration 020 made to `milestone_ref`, for the same
-- reason: once a deferral can be amended, two revisions of one deferral
-- legitimately carry the same `id`, so `id` can no longer be the PRIMARY
-- KEY. `revision_id` takes over as the per-row SCD2 key and `id` becomes
-- the immutable surrogate id every revision shares -- which is also what
-- keeps `milestone_deferral.milestone_id`'s plain-UUID parentage
-- (migration 020's LB2 parentage rule) pointing at one logical deferral
-- lineage rather than at a physical row.
--
-- `milestone_deferral.id`'s parent link is untouched by this migration and
-- needs no second reversal: 010 declared `milestone_id UUID NOT NULL
-- REFERENCES milestone_ref(id)` on the premise that "milestone_ref is not
-- SCD2, so its id is table-wide unique", and migration 020 already dropped
-- that constraint for exactly that reason. This migration's premise is
-- narrower -- it is about the deferral's OWN id, not its parent's -- so
-- there is no referential action here to drop or restore. The down
-- migration below restores this table's pre-023 shape only; the
-- milestone_ref parentage shape is 020's down migration's business, and it
-- runs after this one on the way back down.
--
-- ============================================================================
-- Backfill: valid_from = created_at, valid_to = NULL
-- ============================================================================
-- The ADD COLUMN's DEFAULT NOW() stamps every pre-existing row with this
-- migration's own wall clock, which would make a point-in-time read
-- report a deferral as having started when the schema was migrated rather
-- than when it was authored. `created_at` is the revision's real start and
-- is already NOT NULL on every row (010), so the backfill can use it
-- directly and a pre-existing deferral's history reads true from the
-- moment it was written.
--
-- Every row stays current (valid_to NULL): this migration supersedes no
-- id and closes no row. It changes no rendered roadmap line and retires
-- no deferral.
--
-- ============================================================================
-- Indexes: every index becomes "among current rows"
-- ============================================================================
-- Both of 010's indexes are re-issued with `AND valid_to IS NULL`, the
-- same treatment 020 gave `milestone_ref`'s.
--
-- `milestone_deferral_milestone_idx` is the index behind every read path
-- that exists (ListDeferrals, and GetMilestone through it, and the
-- renderer through that): unfiltered, it would return a milestone's
-- superseded text alongside its current text, and the renderer emits one
-- "Deliberately deferred:" line per row it is handed -- so an amended
-- deferral would double-render in the roadmap and in the `get_milestone`
-- tool response alike.
--
-- `milestone_deferral_scope_idx` is scoped to current rows even though no
-- read path queries `scope_id` today. On an SCD2 table a partial index
-- that silently admits superseded revisions is a trap for the next caller
-- who writes a scope-wide query, and there is no audit read path in
-- krill/store to starve by filtering it -- whereas the milestone_id index
-- above is what makes the current-rows read cheap in the first place.
ALTER TABLE milestone_deferral
    ADD COLUMN revision_id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN valid_from  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN valid_to    TIMESTAMPTZ NULL;

UPDATE milestone_deferral SET valid_from = created_at;

ALTER TABLE milestone_deferral DROP CONSTRAINT milestone_deferral_pkey;
ALTER TABLE milestone_deferral ADD PRIMARY KEY (revision_id);

CREATE UNIQUE INDEX milestone_deferral_current_id_idx ON milestone_deferral(id) WHERE valid_to IS NULL;

DROP INDEX milestone_deferral_milestone_idx;
CREATE INDEX milestone_deferral_milestone_idx ON milestone_deferral(milestone_id) WHERE valid_to IS NULL;

DROP INDEX milestone_deferral_scope_idx;
CREATE INDEX milestone_deferral_scope_idx ON milestone_deferral(scope_id) WHERE valid_to IS NULL;
