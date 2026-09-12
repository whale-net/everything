-- 002_spec_entities: the spec axis's entity model (issue #2488, M1).
-- `Product -> FeatureSet -> Feature -> {FR, NFR}` and
-- `FeatureSet -> LoadBearingDecision`, plus `Product -> Persona` and
-- `Product -> NonGoal` (the brief-document entity kinds this task also
-- settles -- see the note at the bottom of this file). Store layer only;
-- no HTTP surface lands with this migration.
--
-- ============================================================================
-- LB2 -- identity: two ids per row, on purpose
-- ============================================================================
-- Every table below carries:
--   * `id`          -- the immutable surrogate id (PRODUCT.md's LB2). Stable
--                       across every supersession: every revision of the
--                       same logical entity carries the SAME `id`. This is
--                       what a parent FK column, a rendered citation, and an
--                       importer round-trip all key against.
--   * `revision_id` -- the SCD2 row key (this migration's own term for the
--                       "separate SCD2 row key" issue #2488 calls for).
--                       PRIMARY KEY, unique per physical row -- `id` cannot
--                       be the primary key because multiple revisions of one
--                       entity legitimately share it.
-- `id` is NOT declared UNIQUE table-wide (it can't be -- superseded
-- revisions keep the old value). What IS unique is "the current row for a
-- given id", which is exactly the partial index every table gets below:
--   CREATE UNIQUE INDEX ... ON <table>(id) WHERE valid_to IS NULL
-- No table has a `display_number`, `ordinal`-as-identity, or `fr_number`
-- column -- LB2's trap. Sibling order is `position`, an INT that may be
-- freely rewritten and carries no identity meaning whatsoever; a render-time
-- pass derives `FR7`/`C4`/`LB3`-style display numbers from `position`
-- (or from creation order, at the renderer's discretion), never the reverse.
--
-- ============================================================================
-- LB2 -- parentage: single-parent FK to the immutable id, not DB-enforced
-- ============================================================================
-- Every child table's parent column (`feature_set.product_id`,
-- `feature.feature_set_id`, `requirement.feature_id`,
-- `load_bearing_decision.feature_set_id`, `persona.product_id`,
-- `non_goal.product_id`) is a plain UUID column holding the PARENT'S
-- IMMUTABLE `id` -- never an array, never a join table. It is deliberately
-- NOT declared `REFERENCES parent(id)`: Postgres foreign keys require the
-- referenced column to carry a table-wide UNIQUE constraint, and `id` is
-- intentionally not unique table-wide (previous paragraph) -- only "current
-- id" is, via a partial index, and Postgres cannot target a FK at a partial
-- index. Referencing `revision_id` instead would be wrong in the other
-- direction: it would pin a child to one specific parent revision, so
-- superseding the parent (an ordinary, frequent operation) would either
-- orphan every child or require rewriting every child's FK on every parent
-- edit -- exactly what LB2's immutable-id design exists to avoid. The
-- store package (krill/store) is therefore the sole place parent existence
-- is validated (a lookup against the parent's current row, in the same
-- transaction as the child insert) -- documented here so a future migration
-- doesn't "fix" this by attempting a REFERENCES clause that cannot express
-- the invariant it would appear to add.
--
-- ============================================================================
-- LB1 -- scope
-- ============================================================================
-- Every table carries `scope_id UUID NOT NULL REFERENCES scope(id)`, and
-- every uniqueness constraint below is scope-qualified -- even though
-- exactly one `scope` row exists as of this migration (001_scope, #2487).
-- `scope.id` IS unique table-wide (scope is a plain mutable config row, not
-- SCD2 -- see 001_scope.up.sql), so this one IS a real DB-enforced FK.
--
-- ============================================================================
-- LB3 -- SCD2 boundary call, per table
-- ============================================================================
-- Every table below is on the spec axis and is SCD2 per AGENTS.md section
-- SCD2: `valid_from`/`valid_to`, close-and-open writes, a partial index on
-- `(id) WHERE valid_to IS NULL`. The per-table boundary-call comment
-- immediately above each CREATE TABLE says so explicitly, per this issue's
-- own requirement that the boundary be written into schema comments, not
-- inferred -- none of these is a work-axis table (task/queue/claim/lease/
-- attempt/note), which would instead be append-only or append-only-plus-
-- claimed (tools/app_registry's `writeback_outbox` precedent, cited in
-- PRODUCT.md's LB3).
--
-- The write path this migration's shape supports (not implemented until
-- the amend task, per issue #2488):
--   UPDATE <table> SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL;
--   INSERT INTO <table> (id, scope_id, ..., valid_from) VALUES ($1, ...);
-- i.e. the new row's `id` is the SAME value the closed row carried -- a
-- supersession, never a new logical entity.

-- ============================================================================
-- product -- spec axis, SCD2 (LB3)
-- ============================================================================
CREATE TABLE product (
    revision_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id     UUID        NOT NULL REFERENCES scope(id),
    name         TEXT        NOT NULL,
    vision       TEXT        NOT NULL,
    position     INT         NOT NULL DEFAULT 0,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX product_current_id_idx ON product(id) WHERE valid_to IS NULL;
-- Scope-qualified natural-key uniqueness (LB1): two current Products in the
-- same scope cannot share a name, case-insensitively.
CREATE UNIQUE INDEX product_scope_name_current_idx ON product(scope_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- feature_set -- spec axis, SCD2 (LB3)
-- ============================================================================
CREATE TABLE feature_set (
    revision_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id     UUID        NOT NULL REFERENCES scope(id),
    product_id   UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above for why this is not a DB-enforced REFERENCES
    name         TEXT        NOT NULL,
    description  TEXT        NULL,
    position     INT         NOT NULL DEFAULT 0,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX feature_set_current_id_idx ON feature_set(id) WHERE valid_to IS NULL;
CREATE INDEX feature_set_product_current_idx ON feature_set(product_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX feature_set_scope_product_name_current_idx ON feature_set(scope_id, product_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- feature -- spec axis, SCD2 (LB3)
-- ============================================================================
-- Also the entity a capability-map entry (`Cn`) resolves onto -- see the
-- note at the bottom of this file and krill/ARCHITECTURE.md's "Capability
-- map entries, personas, and non-goals" section. No column here says so:
-- a `Cn` citation is just an ordinary Feature `id`, exactly like an `FR`
-- citation is a Requirement `id` -- the resolution is structural (an FK),
-- not a stored tag.
CREATE TABLE feature (
    revision_id     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id        UUID        NOT NULL REFERENCES scope(id),
    feature_set_id  UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above
    name            TEXT        NOT NULL,
    description     TEXT        NULL,
    position        INT         NOT NULL DEFAULT 0,
    valid_from      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to        TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX feature_current_id_idx ON feature(id) WHERE valid_to IS NULL;
CREATE INDEX feature_feature_set_current_idx ON feature(feature_set_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX feature_scope_featureset_name_current_idx ON feature(scope_id, feature_set_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- requirement -- spec axis, SCD2 (LB3)
-- ============================================================================
-- Carries a `kind` discriminator for FR vs NFR rather than being split into
-- two tables: FR and NFR share every other column and every constraint
-- shape (single parent Feature, SCD2, scope-qualified uniqueness), and
-- `kind` is the only axis that differs -- a second table would duplicate
-- this entire block for no behavioral difference. `kind` is NOT a display
-- number (LB2): "FR7" is rendered from `kind='FR'` plus a render-time
-- position among same-kind siblings, never stored as e.g. `fr_number = 7`.
CREATE TABLE requirement (
    revision_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id     UUID        NOT NULL REFERENCES scope(id),
    feature_id   UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above
    kind         TEXT        NOT NULL CHECK (kind IN ('FR', 'NFR')),
    name         TEXT        NOT NULL,
    body         TEXT        NULL,
    position     INT         NOT NULL DEFAULT 0,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX requirement_current_id_idx ON requirement(id) WHERE valid_to IS NULL;
CREATE INDEX requirement_feature_current_idx ON requirement(feature_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX requirement_scope_feature_name_current_idx ON requirement(scope_id, feature_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- load_bearing_decision -- spec axis, SCD2 (LB3)
-- ============================================================================
-- Attaches to the FeatureSet it constrains (FR4's persistence) rather than
-- living in one global list, per PRODUCT.md's C2 -- "so whoever touches
-- that area is shown it and nobody loads the global list." A later
-- many-to-many for this specifically (C23, cross-product decisions, Later)
-- is cheap precisely because a LoadBearingDecision is a leaf nobody walks
-- *through* (PRODUCT.md LB2) -- widening it later does not touch the
-- scoped query any of the other tables above participate in.
CREATE TABLE load_bearing_decision (
    revision_id     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id        UUID        NOT NULL REFERENCES scope(id),
    feature_set_id  UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above
    name            TEXT        NOT NULL,
    body            TEXT        NULL,
    position        INT         NOT NULL DEFAULT 0,
    valid_from      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to        TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX load_bearing_decision_current_id_idx ON load_bearing_decision(id) WHERE valid_to IS NULL;
CREATE INDEX load_bearing_decision_feature_set_current_idx ON load_bearing_decision(feature_set_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX load_bearing_decision_scope_fs_name_current_idx ON load_bearing_decision(scope_id, feature_set_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- Brief-document entity kinds (decided in this task, issue #2488) --
-- persona and non_goal. See krill/ARCHITECTURE.md's "Capability map
-- entries, personas, and non-goals" section for the full decision and its
-- reasoning; the summary is:
--   * persona and non_goal are product-level spec entities, same SCD2 /
--     scope_id / surrogate-id rules as everything above, single-parent FK
--     to `product`.
--   * a capability-map entry (`Cn`) is NOT a fourth parallel table -- it
--     maps onto `feature` (see that table's comment above). No schema
--     change was needed to make "an FR -> C citation resolves to a real
--     entity id" true: `requirement.feature_id` already is that
--     resolution.
-- ============================================================================

-- ============================================================================
-- persona -- spec axis, SCD2 (LB3)
-- ============================================================================
CREATE TABLE persona (
    revision_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id     UUID        NOT NULL REFERENCES scope(id),
    product_id   UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above
    name         TEXT        NOT NULL,
    description  TEXT        NULL,
    position     INT         NOT NULL DEFAULT 0,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX persona_current_id_idx ON persona(id) WHERE valid_to IS NULL;
CREATE INDEX persona_product_current_idx ON persona(product_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX persona_scope_product_name_current_idx ON persona(scope_id, product_id, lower(name)) WHERE valid_to IS NULL;

-- ============================================================================
-- non_goal -- spec axis, SCD2 (LB3)
-- ============================================================================
-- `kind` distinguishes PRODUCT.md's two Non-goals buckets ("Permanent" vs
-- "Explicitly *not* non-goals -- deferred, not foreclosed") -- the same
-- discriminator-column shape as `requirement.kind`, for the same reason
-- (one table, one differing axis, not a second table).
CREATE TABLE non_goal (
    revision_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    scope_id     UUID        NOT NULL REFERENCES scope(id),
    product_id   UUID        NOT NULL, -- parent's immutable `id`; see the LB2 parentage note above
    kind         TEXT        NOT NULL CHECK (kind IN ('permanent', 'deferred')),
    name         TEXT        NOT NULL,
    body         TEXT        NULL,
    position     INT         NOT NULL DEFAULT 0,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX non_goal_current_id_idx ON non_goal(id) WHERE valid_to IS NULL;
CREATE INDEX non_goal_product_current_idx ON non_goal(product_id) WHERE valid_to IS NULL;
CREATE UNIQUE INDEX non_goal_scope_product_name_current_idx ON non_goal(scope_id, product_id, lower(name)) WHERE valid_to IS NULL;
