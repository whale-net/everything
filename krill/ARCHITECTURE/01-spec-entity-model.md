# The spec entity model (LB2/LB3, issue #2488)

Migration 002 (`migrate/schema/migrations/002_spec_entities.up.sql`)
creates the spec axis: `Product -> FeatureSet -> Feature -> {FR, NFR}`,
`FeatureSet -> LoadBearingDecision`, and `Product -> {Persona, NonGoal}`.
Every one of those seven tables shares one shape:

- **Two ids per row.** `id` is the immutable surrogate id (LB2) — stable
  across every supersession of the same logical entity. `revision_id` is
  the SCD2 row key: a separate, table-wide-unique primary key, needed
  because `id` is *not* unique table-wide (every revision of one entity
  shares it) — `revision_id` is. "Current" is `WHERE id = $1 AND
  valid_to IS NULL`, backed by a `UNIQUE` partial index on `(id) WHERE
  valid_to IS NULL` on every table (LB3).
- **No stored display number.** No table has a `display_number`,
  `ordinal`-as-identity, or `fr_number` column — LB2's trap: if a display
  number were the primary key, inserting a sibling or superseding an
  entity would renumber every citation of it in every generated doc, code
  comment, and CI check that greps for it. Sibling order lives in
  `position`, an `INT` that carries no identity meaning and may be
  rewritten freely (inserting between two siblings changes no existing
  sibling's `id`). A render-time pass — not this task — turns `position`
  (or creation order) into `FR7`/`C4`/`LB3`-style numbers.
- **Single-parent FK to the immutable id, not DB-enforced.** Every child's
  parent column (`feature_set.product_id`, `feature.feature_set_id`,
  `requirement.feature_id`, `load_bearing_decision.feature_set_id`,
  `persona.product_id`, `non_goal.product_id`) holds the parent's
  immutable `id` — never an array, never a join table. It is deliberately
  **not** a `REFERENCES` column: Postgres foreign keys require the
  referenced column to carry a table-wide `UNIQUE` constraint, and `id` is
  intentionally not unique table-wide (only "current `id`" is, via a
  partial index, which Postgres cannot target a FK at). Referencing
  `revision_id` instead would be wrong in the opposite direction — it
  would pin a child to one specific parent revision, so superseding the
  parent would either orphan every child or force rewriting every child's
  FK on every parent edit, exactly what an immutable id exists to avoid.
  `krill/store`'s `currentRowExists` helper is therefore where parent
  existence is actually enforced: every `Create` on a child entity checks
  it, inside the same transaction as the `INSERT`. This is a deliberate
  departure from this repo's other SCD2 precedents (`leaflab`'s
  `sensor_region_history`, `tools/app_registry`'s
  `app_manifest_history`), which split an immutable, non-versioned
  identity table from a separate SCD2 history table specifically so a
  plain `REFERENCES` stays possible. That split would have doubled this
  migration's table count (seven tables become fourteen) for a schema
  where every "identity" table would carry nothing but an `id` and a
  `scope_id` — issue #2488 asks for five (seven, counting persona/
  non_goal) tables, not fourteen, and the store-layer check gives up
  nothing observable: a caller still gets an error naming the missing
  parent, just from `krill/store` instead of from Postgres.
- **Scope-qualified everything (LB1).** Every table carries a non-null
  `scope_id REFERENCES scope(id)` (a real FK — `scope.id` *is* unique
  table-wide, since `scope` is not SCD2), and every natural-key uniqueness
  constraint is `(scope_id, ...)`-qualified, e.g.
  `feature_set_scope_product_name_current_idx` on
  `(scope_id, product_id, lower(name)) WHERE valid_to IS NULL`.
- **Per-table LB3 comment.** Each `CREATE TABLE` in migration 002 is
  preceded by a comment stating its boundary call explicitly ("spec axis,
  SCD2") — the boundary is written into the schema, not left for a reader
  to infer, per issue #2488's own requirement.

**What this task does not build.** Supersession (the close-and-open write
— `UPDATE ... SET valid_to = NOW() ...; INSERT ...` with the same `id`) is
explicitly out of scope; `krill/store` ships `Create` and current-value
reads only. The schema already supports the write path — every column a
supersession needs is present — but no store method performs one yet.
That is a later "amend" task (issue #2493, see "Amend and as-of history
reads" below).

