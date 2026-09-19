-- 010_milestone_authoring: the milestone authoring surface (issue #2683,
-- FR1, FR2, C13) -- an outcome sentence, an FR budget, and a
-- deliberately-deferred list on top of the bare `milestone_ref` M1 ships
-- (004_milestone_assoc.up.sql), plus the `relation` discriminator that
-- lets `entity_milestone` carry both delivery-axis lists (Delivers, Must
-- not foreclose) in the one association table LB6 already settled --
-- never a `milestone_id` column added to `feature`, `requirement`, or
-- `load_bearing_decision` (LB6, unchanged by this migration).
--
-- ============================================================================
-- LB3 -- milestone_ref's new columns: plain mutable, not SCD2
-- ============================================================================
-- `outcome` and `fr_budget` are revisable (FR2's SetFRBudget path), but a
-- milestone's outcome sentence or FR budget changing is not a fact worth
-- versioning the way a Requirement's body is -- NFR2's append-only
-- obligation is specifically about *status* history (a later issue on
-- this board), not about every authoring field. Plain mutable columns, no
-- `valid_from`/`valid_to` pair -- the same boundary call `milestone_ref`
-- itself already made (004_milestone_assoc.up.sql's LB3 note).
--
-- `kind` is added now, CHECK-constrained to the one value that exists
-- today ('milestone'), so `ListRefsByProduct`, the renderer, and the
-- importer can be migrated to be kind-aware ahead of any second kind
-- existing (see krill/store/milestone.go and krill/render/store_source.go)
-- -- later issues on this board widen the CHECK, never this one.
--
-- `position` (FR7, issue #2692's nextSiblingPosition helper) orders
-- milestones as authored, mirroring every other spec-entity table's own
-- `position` column added by that task.
--
-- ============================================================================
-- LB4 -- milestone_ref's subject-pair columns are nullable, unlike pointer_artifact's
-- ============================================================================
-- `created_by_acting_*`/`created_by_on_behalf_of_*` mirror
-- `pointer_artifact`'s own LB4 columns (005_pointer_artifact.up.sql) --
-- but, unlike that table, are NULLable here: `krill/importer`'s
-- `GetOrCreateRef` path (FR16) keeps writing `milestone_ref` rows with no
-- session-attributable actor (a batch import has no caller to attribute
-- to), and that path keeps working unchanged per this issue's own
-- Implementation section. A row created through the new authoring path
-- (`CreateMilestone`) always populates both -- NFR4's "every create ...
-- records the acting and on-behalf-of Subject pair" binds the new
-- authoring surface, not retroactively the importer's pre-existing rows.
--
-- ============================================================================
-- milestone_deferral -- one row per deliberately-deferred item, not SCD2 (LB3)
-- ============================================================================
-- A deferral is a fact ("this was deferred to that destination"), not a
-- value that changes over time -- there is no "un-defer" or "revise a
-- deferral" operation in this issue's scope, so this is a plain
-- append-only table, no `valid_from`/`valid_to` pair -- the same boundary
-- call as `pointer_artifact` (005_pointer_artifact.up.sql) and
-- `import_completion` (009_import_completion.up.sql). `milestone_id` IS a
-- real DB-enforced REFERENCES -- `milestone_ref` is not SCD2, so its `id`
-- is table-wide unique, same as `entity_milestone.milestone_id`
-- (004_milestone_assoc.up.sql). `destination` is NOT NULL: FR1 requires
-- every deferred entry to cite where it went (the milestone or `Later`
-- capability bucket it moved to) -- there is no "deferred, destination
-- unknown" row. `created_by_*` here IS mandatory (unlike milestone_ref's
-- own new columns above): every write path onto this brand-new table is
-- the new `AddDeferral` method, which always has a real caller session --
-- there is no pre-existing importer path onto this table to keep
-- unchanged.
--
-- ============================================================================
-- entity_milestone.relation -- LB6, distinguishing Delivers from Must not foreclose
-- ============================================================================
-- Additive discriminator column, defaulting to 'delivers' so every
-- existing (importer-written) row keeps its existing meaning unchanged --
-- 004_milestone_assoc.up.sql's association rows were, until this
-- migration, implicitly all "delivers". `entity_milestone_entity_milestone_idx`
-- is replaced (not just extended) to include `relation`, so the same
-- entity may now appear against the same milestone once per relation (e.g.
-- a Feature the roadmap both delivers under one milestone and separately
-- calls out as must-not-foreclose under another) without a spurious
-- duplicate rejection.
ALTER TABLE milestone_ref
    ADD COLUMN kind                         TEXT NOT NULL DEFAULT 'milestone' CHECK (kind IN ('milestone')),
    ADD COLUMN outcome                      TEXT NULL,
    ADD COLUMN fr_budget                    INT  NULL,
    ADD COLUMN position                     INT  NOT NULL DEFAULT 0,
    ADD COLUMN created_by_acting_iss        TEXT NULL,
    ADD COLUMN created_by_acting_sub        TEXT NULL,
    ADD COLUMN created_by_acting_kind       TEXT NULL,
    ADD COLUMN created_by_on_behalf_of_iss  TEXT NULL,
    ADD COLUMN created_by_on_behalf_of_sub  TEXT NULL,
    ADD COLUMN created_by_on_behalf_of_kind TEXT NULL;

CREATE TABLE milestone_deferral (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    milestone_id                 UUID        NOT NULL REFERENCES milestone_ref(id),
    body                         TEXT        NOT NULL,
    destination                  TEXT        NOT NULL,
    position                     INT         NOT NULL DEFAULT 0,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX milestone_deferral_milestone_idx ON milestone_deferral(milestone_id);
CREATE INDEX milestone_deferral_scope_idx ON milestone_deferral(scope_id);

ALTER TABLE entity_milestone
    ADD COLUMN relation TEXT NOT NULL DEFAULT 'delivers' CHECK (relation IN ('delivers', 'must_not_foreclose'));

DROP INDEX entity_milestone_entity_milestone_idx;
CREATE UNIQUE INDEX entity_milestone_entity_milestone_idx ON entity_milestone(entity_id, milestone_id, relation);
