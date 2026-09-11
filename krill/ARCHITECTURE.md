# krill — Architecture

This document covers what exists after M1's domain scaffolding (issue
#2487) and spec entity model (issue #2488). See [`PRODUCT.md`](PRODUCT.md)
for vision, personas, load-bearing decisions, and the milestone roadmap
that drives what gets built next.

## Component map (as of this task)

```
                 ┌───────────┐
   Postgres  ◄───│  migrate  │  job: applies schema, seeds `scope` (LB1/NFR2)
   (scope)        └───────────┘
        ▲
        │
   ┌────┴────┐
   │   api   │  external-api: /healthz (live DB ping) only, no spec endpoints
   └─────────┘
```

`migrate` and `api` each get their own Postgres connection
(`PG_DATABASE_URL`, `//libs/go/db` / `//libs/go/migrate` — see `ENV.md`).
There is no `mcp` binary yet; `krill/plugin/` exists as a placeholder
directory for its future Claude Code plugin entries (see README.md
"Claude Code plugin"), and `//krill:krill_chart` only bundles `migrate`
and `api` today.

`krill/store` (issue #2488) is the pgx-based repository over migration
002's spec tables — a library, not a binary, so it does not appear in the
component map above. `api` does not import it yet: this task is store
layer only, per its own scope ("no HTTP surface — that is a separate
task"). Nothing in `krill/api/routes.go` changes here.

## The spec entity model (LB2/LB3, issue #2488)

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
That is a later "amend" task.

## Capability map entries, personas, and non-goals (issue #2488)

The importer (FR16) and renderer (FR13) both need product-brief content
that is not in the `Product -> FeatureSet -> Feature -> {FR, NFR}` chain:
capability-map entries (`Cn`), personas, and non-goals. This task decides
how all three map onto the entity model, so the two downstream tasks agree.

**Personas and non-goals are new product-level tables.** `persona` and
`non_goal` (migration 002) are ordinary spec entities — same SCD2 /
`scope_id` / surrogate-id rules as everything else, single-parent FK to
`product`. `non_goal.kind` (`permanent` | `deferred`) carries
`PRODUCT.md`'s Non-goals split ("Permanent" vs "Explicitly *not*
non-goals — deferred, not foreclosed") the same way `requirement.kind`
carries FR vs NFR: one table, one discriminator column, not two tables
that would otherwise be identical.

**A capability-map entry (`Cn`) is not a fourth parallel table — it maps
onto `Feature`.** `product/02-capability-map.md`'s own opening note
already settles the direction: *"Krill's own entity model retires
`Capability` — `Feature` takes its slot in the
`Product -> FeatureSet -> Feature -> {FR, NFR}` chain."* This task's job
is to confirm that decision requires no schema of its own and to record
why:

- An FR's citation of a capability (`FR4 (C3) — ...`) is structurally
  identical to an FR's citation of its parent — both are "this
  Requirement's `feature_id`, resolved against a real row." Making `Cn`
  its own table would mean every citing FR needs *two* parent-shaped
  references (its `Feature` and its `Capability`) for what is, after
  today's brief is imported, the same entity. `feature` already *is* that
  entity once import treats each capability-map entry as one `Feature`
  row. A test asserting this (a later phase of this task): creating a
  `Feature` (standing in for a capability `Cn`) and a `Requirement` under
  it whose `feature_id` equals that `Feature`'s `id` is the whole proof
  that "an FR → C citation resolves to a real entity id rather than a
  string" — no separate association table or citation-string column is
  needed for that to be true.
- `FeatureSet` is *not* where `Cn` maps: the capability map's own
  "Now/Next/Later" grouping is a delivery-axis concept (which milestone
  ships a capability), not a spec-axis grouping — that is LB6's
  association table (migration 004, a later M1 task), never a column or a
  parent choice on `feature`/`feature_set`. `FeatureSet` remains a
  free-form mid-tier grouping of related Features, assigned by whoever
  authors or imports a product's spec (the importer task's job, not
  this one).
- No column anywhere stores `Cn` itself (`capability_number`, `c_number`,
  etc.) — that would be exactly the display-number trap LB2 exists to
  prevent, applied to a different vocabulary. A `Cn` label is rendered
  from a `Feature`'s `position` among its siblings, precisely like an
  `FRn`/`LBn` label is rendered from a `Requirement`/`LoadBearingDecision`'s
  `position`.

## The `scope` table (LB1)

Every entity table this milestone (and every later one) adds carries a
non-null `scope_id` foreign key — including this task, which seeds
`scope` before any entity table exists. `scope`, not any entity row, owns
this repo's forge coordinates:

- `repo_full_name` (e.g. `whale-net/everything`)
- `default_branch` (e.g. `main`)
- `pointer_issue_number` (nullable — unset until a later task's FR20
  pointer-artifact issue exists)

**Why the indirection, not a bare `repo_full_name` column on every
table:** with a natural key, a repo rename is a rewrite of every row that
carries it; with a surrogate `scope_id`, it is a single-row edit on
`scope`. It also gives a later cross-product decision (a `Later` capability
in `PRODUCT.md`) somewhere to live without a migration. M1 seeds exactly
one `scope` row (`krill/migrate/seed`) and exposes no CRUD over it — the
scope selector, per-scope authorization, and whether a scope is a repo or
an org are all deferred to whenever those capabilities are actually
scoped.

**Mutation-shape boundary call (LB3):** `scope` is a plain mutable config
row — not SCD2 (`AGENTS.md` "SCD2" — no `valid_from`/`valid_to`) and not
the work axis's append-only + claimed shape. See the schema comment in
`migrate/schema/migrations/001_scope.up.sql` for the same reasoning
in-line with the DDL it applies to.

## Migration numbering (M1)

Assigned up front in issue #2487 so parallel tasks under the same
milestone never collide on a migration version:

| Version | Contents | Task |
|---------|----------|------|
| `001` | `scope` | #2487 |
| `002` | Spec entities (Product/FeatureSet/Feature/FR/NFR/LoadBearingDecision/Persona/NonGoal) | This task (#2488) |
| `003` | `session` (FR3's `init` gate) | Later M1 task |
| `004` | Milestone reference + association (FR17) | Later M1 task |
| `005` | Pointer artifact (FR20) | Later M1 task |

## Open items

- No HTTP or MCP surface over the spec entity model yet — `krill/store`
  is store-layer only (issue #2488); `api` still exposes nothing beyond
  `/healthz`.
- No supersession/amend write path yet — `krill/store` ships `Create` and
  current-value reads only (see "The spec entity model" above).
- No MCP surface yet — `krill/plugin/` is a placeholder only.
- No auth (NFR1's two-front-door pattern) wired up yet — `api` has no
  authenticated route to gate.
