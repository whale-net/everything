# krill — Architecture

This document covers what exists after M1's domain scaffolding (issue
#2487), spec entity model (issue #2488), and scoped-slice query (issue
#2491). See [`PRODUCT.md`](PRODUCT.md) for vision, personas, load-bearing
decisions, and the milestone roadmap that drives what gets built next.

## Component map (as of this task)

```
                 ┌───────────┐
   Postgres  ◄───│  migrate  │  job: applies schema, seeds `scope` (LB1/NFR2)
   (scope)        └───────────┘
        ▲
        │
   ┌────┴────┐
   │   api   │  external-api: /healthz, /sessions/init, the M1 entity
   └─────────┘  write API (issue #2490 — create/attach only), and the
                FR5-FR9 scoped-slice query surface (issue #2491, read-only)
```

`migrate` and `api` each get their own Postgres connection
(`PG_DATABASE_URL`, `//libs/go/db` / `//libs/go/migrate` — see `ENV.md`).
There is no `mcp` binary yet; `krill/plugin/` exists as a placeholder
directory for its future Claude Code plugin entries (see README.md
"Claude Code plugin"), and `//krill:krill_chart` only bundles `migrate`
and `api` today.

`krill/store` (issue #2488) is the pgx-based repository over migration
002's spec tables — a library, not a binary, so it does not appear in the
component map above. `api` imports it as of issue #2490
(`krill/api/routes.go`'s `store.New(pool)`), behind the entity create/
attach handlers described in "`init` and the write gate" below.
`krill/slice` (issue #2491) is the query layer built on top of `krill/store`,
and `krill/api/handlers` is the HTTP surface over `krill/slice` — both
also wired into `api` via `krill/api/routes.go` (see "The scoped-slice
query" section below).

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
That is a later "amend" task (issue #2493, see "Amend and as-of history
reads" below).

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
| `002` | Spec entities (Product/FeatureSet/Feature/FR/NFR/LoadBearingDecision/Persona/NonGoal) | #2488 |
| `003` | `session` (FR3's `init` gate) | #2489 |
| `004` | Milestone reference + association (FR17) | #2492 |
| `005` | Pointer artifact (FR20) | Later M1 task |

## `krill_session` and the two session ids (FR3, #2489)

Migration `003` adds `krill_session`, the row FR3's `init` primitive
writes and the write gate (`api/handlers/gate.go`, this task) reads. Two
identifiers matter here and must never be confused:

- **`krill_session.id`** — krill's own session identifier, a surrogate
  UUID minted by Postgres (`DEFAULT gen_random_uuid()`) every time `init`
  is called. This is the id the write gate requires on every mutating
  call this milestone exposes.
- **`libs/go/whagent`'s `Claim.WhagentSessionID`** — a *different*
  session concept, scoped to a whagent-net agent run, recorded on
  `krill_session.whagent_session_id` purely as a correlation field. It is
  nullable (a human/OAuth2 caller has none), it is never used as or in
  place of `krill_session.id`, and a whagent-authenticated call still
  gets its own, distinct krill session id. **In M1, `init` takes this
  value as-is from the request body** — `api` mounts no whagent-verifier
  middleware to extract it from a verified `Claim` (see "`init` and the
  write gate" below for why) — so it is only as trustworthy as every
  other field `init` accepts in this milestone.

`krill_session` also carries `acting_*`/`on_behalf_of_*` — two
`(iss, sub, kind)` triples (LB4, mirroring `whagent_net`'s LB2 and
`libs/go/whagent`'s `Claim` shape verbatim), both `NOT NULL`. When a
caller acts for itself the two triples are written identically; the
store layer (`krill/store/session.go`) never infers this — every caller
of `InitSession` passes both explicitly. The table is append-only, not
SCD2 (LB3): M1 ships only `init`, no update path over a session row.

## `init` and the write gate (FR3, #2489, Implementation phase)

`api` now exposes `POST /sessions/init` (`api/handlers/session.go`),
wired in `routes.go`: a caller posts its acting and on-behalf-of `(iss,
sub, kind)` triples (and, optionally, a `whagent_session_id` correlation
value) and gets back the krill-native session id `InitSession` minted.
**`init` is intentionally unauthenticated in M1** — no bearer-token
verification is mounted on the `api` binary (see "No auth wired up on
`api`" below); `init` trusts the caller's asserted identity fields rather
than re-deriving them from a verified credential. This is a deliberate M1
boundary, not an oversight: NFR1's two-front-door pattern (`mcpauth` +
`libs/go/whagent`) is scoped entirely to the separate `krill/mcp` binary
(issue #2494), which never touches `krill_session` — `api`'s HTTP surface
has no equivalent front door in this milestone.

`api/handlers/gate.go` is the write gate every mutating endpoint in this
milestone passes through (FR3's "write-only" clause): `RequireSession`
wraps a handler, requires the `X-Krill-Session-Id` header to name a row
`init` actually minted (via `SessionStore.GetSession`), and — on success —
resolves that session's two subjects and scope onto the request context
(`SessionFromContext`) for the wrapped handler to read. It rejects with
401 on a missing header, a malformed id, or an id `GetSession` cannot
find. `RequireSession` covers exactly six endpoints across this
milestone — entity creates (FR1, FR2), LB attach (FR4), amend (FR12,
issue #2493), import (FR16, issue #2492), and pointer-issue create (FR20,
issue #2496) — and no read path, including FR21's live C3 query. The
entity creates and LB attach are wired in `routes.go` as of issue #2490
(`api/handlers/product.go`, `featureset.go`, `feature.go`,
`requirement.go`, `decision.go`); amend is wired as of this task (issue
#2493, `api/handlers/amend.go` — see "Amend and as-of history reads"
below); import/pointer-issue-create remain unwired until their own tasks
land.

## The scoped-slice query (LB7, issue #2491)

`krill/slice` implements FR5-FR9 — the C3 scoped query — over
`krill/store`'s spec entity model. `krill/api/handlers` is the HTTP
surface over it, wired into `api`'s mux in `routes.go`.

**One document type, four granularities.** `slice.Document` (`document.go`)
is the single typed, self-describing shape every granularity returns:

- `GetFeatureSetSlice` (FR5) — a FeatureSet, its Features, their FRs/NFRs,
  and *only* the LoadBearingDecisions attached to that FeatureSet — never
  the product-wide decision list.
- `GetFeatureSlice` (FR6) — a Feature and its FRs/NFRs, nothing else.
- `GetRequirementSlice` (FR7) — a single FR or NFR by surrogate id alone.
- `GetProductSlice` (FR8) — every FeatureSet, Feature, FR, NFR, and
  LoadBearingDecision beneath a Product, in one call.

`Document` does not vary by granularity (FR9): it carries an explicit
`schema_version` (`slice.SchemaVersion`), and every included entity embeds
an `EntityRef` — its surrogate id (LB2) and the as-of revision (SCD2
`revision_id`) it was assembled from. A granularity that doesn't reach a
given entity kind simply leaves that field of `Document` empty; there is
no second response type. **This is the payload M4's claim will later
enrich (LB7)** — not a UI response shape, and not something a later task
should fork into a per-consumer projection. Display numbers are
deliberately absent from `Document` today; if a later consumer needs one,
it is computed at assembly time from a sibling's `Position`, never read
from a stored column (LB2) — see `document.go`'s comment on
`ProductEntity.Position` for where that boundary is written down.

**Read-only, no `init` gate.** None of the four query methods or their
HTTP routes check `session` — FR3's `init` gate is write-only, and read
paths never require it (root plan issue #2485). `krill/store`'s existing
`ListCurrentBy*`/`GetCurrentByID` methods cover three of the four
granularities directly; `krill/store/slice.go`'s `SliceStore` adds the
cross-table joins (`ListRequirementsByFeatureSet`,
`ListFeaturesByProduct`, `ListRequirementsByProduct`,
`ListDecisionsByProduct`) that `GetFeatureSetSlice` and `GetProductSlice`
need and no single entity's `*Store` owns on its own — one query per
entity kind rather than one query per sibling, regardless of how many
FeatureSets or Features exist beneath the requested id.

## The markdown importer and the delivery-axis association (FR16, FR17, issue #2492)

`krill/importer` (a library) and `krill/importer/cmd` (its runnable
entrypoint, `bazel run //krill/importer/cmd:import -- --path <dir>
--session-id <uuid>`) are the one-way markdown importer LB5 and PRODUCT.md's
C8 describe: it parses a `PRODUCT.md` + `product/*.md` doc set (the layout
`tools/project-manager/CONVENTIONS.md` § Layout defines) into
`krill/store`'s spec entities and prints FR16's entity-id report.

**This is the only code path in `krill/` that ever parses a committed
markdown document back into entities (LB5, FR15) — `krill/importer/
importer.go`'s package doc states this explicitly and names FR15.** A
future renderer (FR13, a separate task) only ever writes markdown from
krill's entities; neither it nor anything else in this tree reads a
committed doc the other direction. Within the importer itself, `parse.go`
never touches `krill/store` — it is a pure text-to-`ParsedProduct` function,
safe to run against a document this milestone does not import (see the
Testing section's `whagent_net` parse-fixture use) — and `write.go` is the
only file that calls a `Create`/`GetOrCreateRef` method, so a parse failure
never leaves a partial product behind.

**Gating.** Import is one of the six write paths `api/handlers.
RequireSession`'s doc comment names (FR3), but it is a CLI entrypoint, not
an HTTP handler, so it cannot literally wrap itself in that middleware.
`importer.Import` performs the same check directly against
`store.SessionStore.GetSession` (`requireSession` in `importer.go`) before
parsing or writing anything, and writes into the session's own `ScopeID` —
a caller with no valid krill session cannot import regardless of which
front door it comes through.

**Where a capability-map entry, a persona, and a non-goal land.** Personas
and non-goals map directly onto `persona`/`non_goal` under the imported
`Product` (issue #2488's decision). A capability-map entry (`Cn`) becomes a
`Feature`, grouped under a `FeatureSet` named for its bucket (`Now`,
`Next`, `Later`) — the bucket is a delivery-axis grouping, not a spec-axis
one, but a `FeatureSet` has to be *something* and "which bucket a
capability was in" is the only grouping the source document offers.
Load-bearing decisions have no natural bucket of their own, so the importer
gives every imported product one synthetic `FeatureSet` named "Load-bearing
decisions" (`loadBearingFeatureSetName` in `write.go`) to hold them,
created once per product and reused on a second import — never guessed
per-entry from which capability an `LBn`'s prose happens to mention.

**Milestone references and the delivery-axis association (LB6).** For each
`### M<n> — ...` roadmap heading, the importer resolves (creating on first
reference) a `milestone_ref` row scoped to `(scope, product, "M<n>")` via
`MilestoneStore.GetOrCreateRef`, then, for every capability id in that
milestone's own `Delivers:` line and every decision id in its own `Must not
foreclose:` line, adds one `entity_milestone` row via
`MilestoneStore.AddAssociation` — `(Feature.ID or LoadBearingDecision.ID,
milestone_ref.ID)`. Both methods are upserts (`ON CONFLICT DO NOTHING`), so
importing the same document twice does not duplicate either table. A
`Delivers:`/`Must not foreclose:` line's trailing prose explanation (e.g.
krill's own "— all seven, each for its own reason:" continuation) is never
scanned for ids — only the token list before the first dash on that same
line counts, so a continuation line's own cross-references to other
capabilities or decisions are never mistaken for this milestone's own list.
See migration `004_milestone_assoc.up.sql`'s LB6 note for the schema side
of this: `milestone_ref` carries only the bare `M<n>` identifier — no
status, no milepebbles, no authoring surface (those are M3's, C13/C28) —
and `entity_milestone` is the association, never a `milestone_id` column on
`feature` or `load_bearing_decision`.

**Fail loudly on an undefined milestone (FR17).** Beyond the per-milestone
`Delivers:`/`Must not foreclose:` pass, the importer scans the whole
roadmap document for every bare `M<n>` token — heading, prose, anywhere —
and returns a non-zero-exit error naming any token with no corresponding
`### M<n>` heading in that same document. This is what "the source names a
milestone its own roadmap section never defines" (the issue's own phrasing)
resolves to: a document is well-formed on this axis exactly when every
`M<n>` it mentions is also a milestone it defines.

## Amend and as-of history reads (FR11, FR12, issue #2493)

`krill/store/amend.go` (write) and `krill/store/history.go` (read) are the
SCD2 supersession pair `AGENTS.md`'s "SCD2" section describes, applied to
the two entity kinds this task scopes for amendment: `Requirement` (FR/NFR)
and `LoadBearingDecision`. No new migration lands with this task — every
column both files need was already present in migration 002 (see "What
this task does not build" above, now resolved).

**Amend (`AmendStore`, FR12) is the close-and-open write**, exactly the two
statements `AGENTS.md` names:

```sql
UPDATE <table> SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL;
INSERT INTO <table> (id, ..., scope_id) VALUES ($1, ..., $scope);
```

`AmendRequirement`/`AmendLoadBearingDecision` first `SELECT ... FOR UPDATE`
the current row inside the same transaction as the close-and-open pair —
this locks it for the transaction's duration so a concurrent amend of the
same `id` cannot race the `UPDATE` or the `(id) WHERE valid_to IS NULL`
partial unique index both amend and Create rely on. The new row carries
the closed row's `id`, `scope_id`, parent id (`feature_id` /
`feature_set_id`), `kind` (Requirement only), and `position` forward
unchanged — an amend replaces `name`/`body` only, never a parent id
(reparenting stays the separate, still-unbuilt operation store/decision.go's
`Create` doc comment describes) and never `position` (so no sibling's
rendered display number moves, per LB2). The surrogate `id` is never
reissued (LB2) and no sibling row of any kind is read or written by an
amend — only the one entity's own current-and-then-superseded rows.

**History reads (`HistoryStore`, FR11)** are the read side, over the same
two tables:

- **As-of** (`GetRequirementAsOf` / `GetLoadBearingDecisionAsOf`) is
  `AGENTS.md`'s "Value at time T" query verbatim:
  `WHERE id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)`.
  Returns `ErrNotFound` if `asOf` predates the entity's first revision (or
  `id` never existed) — there is no row satisfying the interval in that
  case, never a zero-value success.
- **Version list** (`ListRequirementVersions` /
  `ListLoadBearingDecisionVersions`) returns every revision sharing `id`,
  oldest first (`ORDER BY valid_from`) — every prior version's own
  `ValidFrom`/`ValidTo` is exactly what a caller needs to see what
  superseded what, and when.

Neither `AmendStore` nor `HistoryStore` reads or writes anything beyond
`requirement`/`load_bearing_decision` — no other entity kind (Product,
FeatureSet, Feature, Persona, NonGoal) is amendable or as-of-readable in
this milestone.

**HTTP surface.** `krill/api/handlers/amend.go` wraps `AmendStore` behind
`RequireSession` (`routes.go`: `POST /requirements/{id}/amend`,
`POST /load-bearing-decisions/{id}/amend`) — one of this milestone's write
paths, exactly like entity create/attach. `krill/api/handlers/history.go`
wraps `HistoryStore` with no session gate at all (`GET
/requirements/{id}/as-of?at=<RFC3339>`, `GET /requirements/{id}/versions`,
and the `load-bearing-decisions` equivalents) — FR11 is a read path, and
read paths never require `init` (root plan issue #2485), exactly like
`krill/slice`'s four granularities.

## Open items

- The HTTP surface over the spec entity model covers create/attach, amend,
  and history reads (issue #2490: `POST /products`, `/feature-sets`,
  `/features`, `/requirements`, `/load-bearing-decisions`; issue #2493:
  `POST /requirements/{id}/amend`, `POST
  /load-bearing-decisions/{id}/amend`, `GET /requirements/{id}/as-of`,
  `GET /requirements/{id}/versions`, and the `load-bearing-decisions`
  equivalents) — no surface at all yet for Persona/NonGoal (`krill/store`'s
  `PersonaStore`/`NonGoalStore` are store-layer only, issue #2488), and no
  amend/history surface for Product, FeatureSet, or Feature (issue #2493
  scopes FR11/FR12 to Requirement and LoadBearingDecision only). FR5-FR9's
  read path exists (issue #2491, see "The scoped-slice query" above); FR21
  remains open.
- No as-of (historical) *slice* read yet — `krill/slice`'s four
  granularities always read current (`valid_to IS NULL`) rows; issue
  #2493's as-of read (see "Amend and as-of history reads" above) is a
  single-entity (`Requirement`/`LoadBearingDecision`) read, not a
  `slice.Document` assembled as of a past revision — that remains open if
  a later task needs it.
- `init` (FR3, #2489) and the write-only gate (`api/handlers/session.go`,
  `api/handlers/gate.go`) cover entity creates and LB attach as of #2490,
  and amend as of #2493 (`api/handlers/amend.go`); pointer-issue create
  (#2496) remains unwired until its own task lands. Import (#2492) is
  gated too, but as a CLI entrypoint checking the session directly against
  the store rather than through this HTTP middleware (see "The markdown
  importer" above).
- No MCP surface yet — `krill/plugin/` is a placeholder only; a later
  milestone's MCP tool wraps `krill/slice` directly, per LB7.
- No auth wired up on `api` — `POST /sessions/init`, every future write
  endpoint, and the FR5-FR9 slice routes all trust caller-asserted
  identity or are unauthenticated (see "`init` and the write gate"
  above); only `krill/mcp` (issue #2494) gets NFR1's two-front-door
  pattern, and only for the read-only spec surface.
- No renderer yet (FR13, a separate task) — the importer's report (FR16)
  is the only reflection of an imported product's entities back to a
  human today; nothing regenerates `PRODUCT.md`/`product/*.md` from
  `krill/store` yet.
