# krill — Architecture

This document covers what exists after M1's domain scaffolding (issue
#2487), spec entity model (issue #2488), scoped-slice query (issue
#2491), the markdown importer (issue #2492), the MCP spec surface (issue
#2494), and the doc renderer (issue #2495). See
[`PRODUCT.md`](PRODUCT.md) for vision, personas, load-bearing decisions,
and the milestone roadmap that drives what gets built next.

## Component map (as of this task)

```
                 ┌───────────┐
   Postgres  ◄───│  migrate  │  job: applies schema, seeds `scope` (LB1/NFR2)
   (scope)   ▲    └───────────┘
        │    │
   ┌────┴────┬────────────┬────────────┐
   │   api   │    mcp     │     ui     │
   └─────────┴────────────┴────────────┘
        ▲            ▲            ▲
        │            │            │
        │            │       external-api: barebones Keycloak sign-in
        │            │       shell; mounts mcpauth's /authorize, /token,
        │            │       /register, and discovery -- the SignInURL
        │            │       mcp's mcpauth front door redirects to
        │            │
        │       external-api: the FR10/NFR1 spec surface -- /mcp/spec,
        │       the same FR5-FR9 slice query as `api`'s /slices/...
        │       routes, over MCP
        │
   external-api: /healthz, /sessions/init, the M1 entity write API (issue
   #2490 — create/attach only), and the FR5-FR9 scoped-slice query surface
   (issue #2491, read-only, also reachable via `api`'s own HTTP routes)
```

`migrate`, `api`, `mcp`, and `ui` each get their own Postgres connection
(`PG_DATABASE_URL`, `//libs/go/db` / `//libs/go/migrate` — see `ENV.md`).
`ui` additionally owns the `ui_sessions` table (migration 007) and, jointly
with `mcp`, the `mcp_credential`/`mcp_oauth_client`/`mcp_auth_code` tables
(migration 006) -- see "krill/ui and the mcpauth front door" below.
`krill/plugin/` now carries `mcp`'s Claude Code plugin entries (`.mcp.json`
/ `mcp_config.json`, issue #2494) rather than being a placeholder, plus a
companion `plugin/data/` "-data" plugin for direct Postgres access to the
same database (see README.md "Claude Code plugin"), and
`//krill:krill_chart` bundles all three binaries.

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
| `005` | Pointer artifact (FR20) | #2496 |
| `006` | mcpauth credential/client/auth-code (`mcp_credential`, `mcp_oauth_client`, `mcp_auth_code`) | mcpauth auth-flow gap |
| `007` | UI sessions (`ui_sessions`, `//libs/go/htmxauth`) | mcpauth auth-flow gap |

## Migration numbering (M2)

Assigned up front in issue #2542, but M1's own mcpauth-auth-flow-gap work
(`006_mcpauth_credential`, `007_ui_sessions` above) landed on `main` first
and claimed `006`/`007` before M2's tasks merged -- M2 renumbers to the
next free slots so parallel tasks under M2 never collide on a migration
version:

| Version | Contents | Task |
|---------|----------|------|
| `008` | `design_session` + `revision_event` (FR1-FR4, NFR1) | #2542 |
| `009` | Import-completion marker (FR12) | filed separately on this plan |

## `design_session` vs `krill_session` (FR1, FR8, #2542)

M2's `design_session` (migration `008`) is the single most confusable
thing in this milestone relative to M1's `krill_session` (migration `003`)
-- write this down explicitly, mirroring "`krill_session` and the two
session ids" below:

- **`krill_session`** is the write-gate row FR3's `init` mints per
  mutating call: one fixed acting/on-behalf-of pair, gating exactly one
  request. It is minted fresh every time a caller calls `init`, and it
  never accumulates state of its own beyond that one pair.
- **`design_session`** is the longer-lived container FR2's
  `revision_event` rounds accumulate under. A single `design_session`
  spans many separate `krill_session`-gated calls, from potentially
  different actors, over its lifetime -- a producer-role Agent's `draft`,
  an architect's `reconciliation`, and a Requirement Contributor's
  `answer` are three different calls, each gated by its own, distinct
  `krill_session`, all landing `revision_event` rows against the same
  `design_session`.

`design_session.opened_by_krill_session_id` records which `krill_session`
gated the `open` call that created the row -- **provenance only**. It is
never the source of a later `revision_event`'s own attribution:
FR2 requires every `revision_event` to carry its own `acting_*`/
`on_behalf_of_*` pair directly (sourced from whichever `krill_session`
gated *that* event's call), so resolving a round's attribution by
following `opened_by_krill_session_id` back through `design_session` would
be wrong the moment a session's second round is written by a different
actor than its first.

`design_session` is append-only, not SCD2 (LB3) -- migration
`008_design_session.up.sql`'s comment: M2 ships no update path over this
row, and its mutable state (how many rounds it has seen, what those rounds
said) is entirely derived from its `revision_event` log, never written
back onto the `design_session` row itself.

**FR8's opening submission** (`design_session.opening_submission`) is a
column on `design_session`, not a sixth `revision_event.event_type` value
and not a `draft` event with empty deltas. FR2's `event_type` enum is
closed at exactly five values (`draft`, `reconciliation`, `answer`,
`signoff`, `ruling`), none of which names a Requirement Contributor's raw,
pre-entity idea -- widening that enum for this would contradict the plan
that fixes it, and reusing `draft` would misattribute a contributor's
plain language as a producer-role Agent's proposal (FR9/FR10 make `draft`
specifically the Agent's act).

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

## The MCP spec surface (FR10/NFR1, issue #2494)

`krill/mcp` exposes the FR5-FR9 scoped-slice query (`krill/slice.Querier`,
above) over MCP, so any MCP-capable harness (Claude Code today) reaches
it with no krill-specific harness code -- FR10's own wording. It mirrors
the two-front-door pattern already shipped in `audience_score_system/mcp`
and `whagent_net/mcp` rather than inventing a third shape.

**One mount point, four thin-wrapper tools.** `krill/mcp/tools/slice.go`
registers `get_feature_set_slice` (FR5), `get_feature_slice` (FR6),
`get_requirement_slice` (FR7), and `get_product_slice` (FR8) — each takes
a single surrogate id (LB2) and calls the matching `slice.Querier` method
directly, returning its `slice.Document` **unchanged**. This is LB7's
"M1's MCP tool is a thin wrapper over it, not the thing itself" applied
literally: no tool file defines its own output struct, so the MCP
response and `api`'s own `GET /slices/...` HTTP response are the same
Go value serialized twice, never two independently-maintained shapes that
could silently drift. All four tools are mounted at `krill/mcp/server`'s
`specMountPath` (`/mcp/spec`) — its own pre-filtered endpoint, following
`whagent_net`'s `/mcp/readonly` vs `/mcp/ops` split
(`whagent_net/ARCHITECTURE.md` "Domain-owned MCP servers and the tool
contract"): the future work-axis surface (M4) gets its own mount
(`/mcp/work`, not built yet) rather than every granularity ever landing on
bare `/`. No write tool is registered on this endpoint in M1 — there is
no `RegisterWrite` in `krill/mcp/server` at all, unlike
`audience_score_system/mcp/server/registry.go`'s `RegisterRead`/
`RegisterWrite` pair; write tools are out of scope until a later
milestone actually needs one.

**Two front doors, one mount point, authorized by persona (NFR1).**
`krill/mcp/server/auth.go` (mcpauth/human) and `whagent_auth.go`
(whagent-net/agent) are structured identically to
`audience_score_system/mcp/server`'s own `auth.go`/`whagent_auth.go`
split — `DualAuthHTTPHandler` routes each request to exactly one door by
bearer-token *shape* (a whagent Claim is always a three-segment JWT; an
mcpauth credential is always a 64-character hex string with no dots),
never by trial-and-error against both verifiers. The one deliberate
departure from that precedent: NFR1 authorizes by **persona** (Swarm
Operator / Requirement Contributor / Agent — `krill/PRODUCT.md`'s
Personas section), not by individual identity, so there is no
`store.Person`/`PersonStore` anywhere in `krill/mcp` — `server.Persona`
is the only identity-shaped value either middleware ever places on
context. Today that resolution is fixed, not looked up: the mcpauth door
always resolves `PersonaSwarmOperator`, the whagent door always resolves
`PersonaAgent`. This is not an oversight — `PRODUCT.md` is explicit that
"The Requirement Contributor exists in the model and in permissions from
M1, but has no unmediated path into krill until C12 lands in M2", so
there is no second human persona for M1's mcpauth door to distinguish,
and a whagent Claim never carries a human profile to resolve further
(`//libs/go/whagent`'s FR10). `krill/mcp/server/registry.go`'s
`RegisterRead` requires only that *some* Persona resolved before a tool
handler runs — none of the four FR5-FR8 tools is persona-sensitive, so
there is no per-tool allow-list yet either; that is expected to change
once the work-axis surface (M4) lands a persona-restricted tool.

**The mcpauth door's migration (`006_mcpauth_credential`) now exists.**
`libs/go/mcpauth.NewCredentialStore` preflights a `mcp_credential`-shaped
table at boot, exactly like `audience_score_system`'s migration 006 and
`whagent_net`'s migration 004; migration 006 now provides it. Until it is
applied against a given deployment, `krill/mcp/main.go` still degrades
rather than failing to boot entirely (which would also break the agent
door, which does not need Postgres at all): a failed `NewCredentialStore`
call logs a warning and substitutes `rejectingCredentialStore`, a
`CredentialStore` of last resort whose every method fails with the same
opaque error `mcpauth.TokenVerifier` already produces for a revoked
credential — so a caller presenting an mcpauth-shaped token against a
not-yet-migrated deployment gets a clean 401, never a panic on a nil
interface. The agent door is unaffected either way.

## krill/ui and the mcpauth front door (the auth-flow gap)

Landing the migration above was necessary but not sufficient: `mcp`'s
mcpauth door verifies credentials, but nothing in M1 ever *minted* one.
`krill/mcp/main.go` constructs no `mcpauth.Provider` — only
`mcpauth.NewCredentialStore` (verification) — so there was no
`/authorize`, `/token`, `/register`, or discovery metadata anywhere in
krill, and every other domain's own front door (`audience_score_system`,
`whagent_net`) solves this by setting `mcpauth.ProviderConfig.SignInURL`
to its own web UI's `/login` route (`libs/go/mcpauth/authorize.go`:
`/authorize` 401s outright when `SignInURL` is unset and the caller isn't
already resolved). krill had no UI to point at — `PRODUCT.md`'s roadmap
defers a real web UI to "Later" (C19).

`krill/ui` (`krill/ui/main.go`) is the minimum viable fix: a standalone
binary that does nothing but (1) Keycloak sign-in via `//libs/go/htmxauth`
(migration `007_ui_sessions`) and (2) construct and mount an
`mcpauth.Provider` (migration `006_mcpauth_credential`, shared with `mcp`)
with `SignInURL: "/login"`. `krill/ui/mcpauth.go`'s `mcpCallerResolver`
reads the signed-in operator's session and encodes their `(iss, sub)` pair
via the new `//krill/identity` package (mirroring
`whagent_net/mcpidentity`'s encoding exactly) — krill has no person/user
table to key an identity to instead (NFR1). This completes the
authorization-code + PKCE round trip end to end (discovery → registration
→ sign-in → `/authorize` → `/token` → a credential `mcp`'s
`NewCredentialStore` can verify), but deliberately does **not** touch
persona resolution: `krill/mcp/server/auth.go`'s `PersonaMiddleware` still
resolves every mcpauth-authenticated caller to `PersonaSwarmOperator`
unconditionally, exactly as before. Widening that resolution to a real
identity → persona lookup is C12's own job (`PRODUCT.md`'s M2), not this
gap-fix's — see `auth.go`'s doc comment.

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

**As-of slice assembly (`krill/slice`).** Every one of C3's four
granularities (`GetFeatureSetSlice`, `GetFeatureSlice`,
`GetRequirementSlice`, `GetProductSlice`, issue #2491) has an `*AsOf` twin
(`GetFeatureSetSliceAsOf`, ..., `krill/slice/query.go`) that assembles the
same `Document` shape as of a past `asOf` instead of today: every
`Requirement`/`LoadBearingDecision` in the result is read through
`HistoryStore` (the revision current at `asOf`, not the latest), and any
entity whose first revision postdates `asOf` is dropped from the
assembly rather than reported at its current contents. `Product`,
`FeatureSet`, and `Feature` have no write path that supersedes a row yet
(no other entity kind is amendable, per the paragraph above), so for
those three "as of `asOf`" reduces to "had it been created by `asOf`"
(`entityExistedAsOf`) — the current row is their only revision, and a
top-level `*AsOf` call whose own entity postdates `asOf` returns
`store.ErrNotFound`, exactly like `HistoryStore`'s own not-found
semantics. `EntityRef.RevisionID` (`krill/slice/document.go`) is the
"as-of revisions" metadata PRODUCT.md's LB7 describes — an `*AsOf`
assembly's entities simply carry a historical row's `RevisionID` instead
of today's current row's. No new HTTP route exists for this yet — the
capability lives at the `slice.Querier` layer only, for a later task's
surface to wire up if needed.

**HTTP surface.** `krill/api/handlers/amend.go` wraps `AmendStore` behind
`RequireSession` (`routes.go`: `POST /requirements/{id}/amend`,
`POST /load-bearing-decisions/{id}/amend`) — one of this milestone's write
paths, exactly like entity create/attach. `krill/api/handlers/history.go`
wraps `HistoryStore` with no session gate at all (`GET
/requirements/{id}/as-of?at=<RFC3339>`, `GET /requirements/{id}/versions`,
and the `load-bearing-decisions` equivalents) — FR11 is a read path, and
read paths never require `init` (root plan issue #2485), exactly like
`krill/slice`'s four granularities.
## The doc renderer (FR13-FR15, NFR3, issue #2495)

`krill/render` (a library) and `krill/render/cmd` (its runnable entrypoint,
`bazel run //krill/render/cmd:render -- --product krill --out krill/`) are
the mirror image of `krill/importer`: they project a Product's current spec
back out to `PRODUCT.md` + `product/*.md` (the same layout the importer
reads), and never the other direction.

**FR15/LB5 -- structurally one-way.** `render.Render` takes a `Source`
interface (`render.go`) whose every method is a read (`GetProductSlice`,
`ListPersonas`, `ListNonGoals`, `ListMilestoneRefs`,
`ListMilestoneAssociations`) -- there is no write method anywhere in that
interface, and `render.go` itself never imports anything capable of calling
one. `store_source.go`'s `StoreSource` is the only file in the package that
holds a `*store.Store` (which does expose `Create`); it exists solely to
adapt one into a `Source`, so a caller wiring up `krill/render/cmd` can only
ever hand `Render` the narrow read surface, never the concrete store.

**FR14 -- citations are computed at render time, never stored.** `Cn`
(a Feature) and `LBn` (a LoadBearingDecision) are both numbered by
`numberByOrder`, a 1-based index over whatever order `krill/store`'s own
queries already return -- `position` then `name` (see "The spec entity
model" above). Nothing in `krill/render` reads or writes a display-number
column, because none exists. A `LoadBearingDecision.Name` occasionally
carries a stale citation baked in by the importer's own parsing (it keeps
a decision's whole source title line, `LB1 — ...`, as `Name`) --
`cleanDecisionTitle` strips that leading token before the renderer
re-prefixes it with the freshly computed number, so a renumber is never
masked by what the importer happened to store.

**Milestones are reconstructed from associations, never authored.**
`renderMilestones` enumerates a product's `milestone_ref` rows
(`MilestoneStore.ListRefsByProduct`, added by this task alongside the
renderer) and, per milestone, resolves its `entity_milestone` rows against
the already-numbered Feature/Decision sets to rebuild `Delivers:` and
`Must not foreclose:` lines. This carries no milestone title, outcome
sentence, or FR budget -- migration 004 stores none of those (see "The
markdown importer" above), so a milestone's rendered entry is deliberately
thinner than a hand-authored roadmap section until a later milestone (M3,
C13/C28) adds an authoring surface.

**NFR3 -- the generated-doc carve-out.** `/AGENTS.md` § Documentation
Conventions now carries a "Generated-doc carve-out" naming
`PRODUCT.md`/`product/*` as non-hand-editable once a domain's brief lives
in krill, and every file `krill/render` writes carries a `GENERATED by
krill/render` header (`render.GeneratedMarker`) naming the entity and the
render revision it came from, mirroring that carve-out at the point of
editing.

**What the renderer does not do.** It renders only the product doc set
(`ARCHITECTURE.md`/`README.md`/`ENV.md`/`TOC.md` stay hand-written, per
krill's own permanent non-goal). It renders no `Requirement` (FR/NFR):
the product brief layout has zero FRs by design (`tools/project-manager/
CONVENTIONS.md` § Product brief & milestones), so `slice.Document`'s
`Requirements` field is read by `GetProductSlice` but never rendered here.

## The GitHub pointer artifact (FR20, C9, issue #2496)

`krill/forge` and `krill/store/pointer.go` implement FR20: `POST
/pointer-artifacts` (`krill/api/handlers/pointer.go`) mints krill's one
thin GitHub issue for a Product, so this repo's own "Part of #\<n\>"
cross-linking convention keeps working once a Product's spec lives in
krill's entity model instead of a markdown file.

**One artifact per Product, not one per PR/commit/conversation.** Despite
`pointer_artifact`'s columns reading like a per-cross-link record at first
glance, this task settles the opposite design: krill creates exactly one
GitHub issue per Product (`pointer_artifact_product_idx`, migration 005,
is a UNIQUE index on `product_id`) and then gets out of the way — the
issue's own number is what a PR body, a commit message, or a conversation
references afterward, through GitHub's ordinary mechanics, with **no
further krill involvement and no per-reference row**. This matches C20
("krill does not own branch or PR lifecycle... stores references only"):
there is no branch name, PR number, commit SHA, or conversation URL column
anywhere in migration 005 — see that migration's own comment for the full
reasoning. `pointer_artifact.kind` discriminates the artifact's own shape
(today, always `"github_issue"`), the same one-column-not-two-tables
precedent as `requirement.kind`/`non_goal.kind`, not what has since
referenced the issue.

**`scope.pointer_issue_number` is the system of record; `pointer_artifact`
is the audit trail.** LB1 already put the forge coordinates
(`repo_full_name`, `default_branch`, `pointer_issue_number`) on `scope`,
not on any entity row (migrations/001_scope.up.sql). `PointerArtifactStore
.Create` (`krill/store/pointer.go`) writes both in one transaction: the
`pointer_artifact` row (who created it — both LB4 subjects, always
recorded, unlike every other M1 create endpoint — and when) and
`scope.pointer_issue_number` (what the current coordinate actually is).
The two can never observably diverge for the reason above: at most one
pointer issue is ever minted per Product in M1's one-Product-per-scope
shape.

**Order of operations avoids minting a spurious issue on a caller
error.** `CreatePointerArtifactHandler` reads back the target Product and
rejects an unknown or cross-scope `product_id` with 400 *before* ever
calling `krill/forge.Client.CreateIssue` — unlike every entity create
handler, this one's store call is not the first fallible step, because its
side effect (a real, human-visible GitHub issue) is not one a rejected
request should still cause.

**`krill/forge` is intentionally the smallest possible client.**
`forge.Client` has exactly one method, `CreateIssue`; `GitHubClient`
authenticates with a plain bearer token (`KRILL_GITHUB_TOKEN`, see
`ENV.md`), not a full GitHub App installation-token flow like
`tools/app_registry/worker/release`'s `GitHubDispatcher` — that
machinery exists to dispatch and poll CI workflow runs repeatedly; FR20
needs exactly one write, ever, per Product.

**Retrievable from the whole-product slice (FR8).** `krill/slice`'s
`GetProductSlice` is the only one of the four granularities that populates
`Document.PointerArtifacts` (`krill/slice/query.go`) — a pointer
artifact's single parent is the Product itself, never a FeatureSet or
Feature, so it is unreachable from `GetFeatureSetSlice`/`GetFeatureSlice`/
`GetRequirementSlice` the way a FeatureSet-scoped LoadBearingDecision is.
`PointerArtifactEntity` (`krill/slice/document.go`) embeds only `ID`, not
the `EntityRef`/`RevisionID` pair every other slice entity carries —
`pointer_artifact` is not SCD2 (LB3), so there is no revision to expose.

## The design skill's live milestone read (FR21, root plan issue #2485)

FR21 is M1's one concrete self-hosting *consumer*: `/project-manager:design
--milestone`'s milestone-read step (`tools/project-manager/skills/design/
SKILL.md` step 2, and `tools/project-manager/agents/producer.md`'s
"Milestone-scoped intake") reads `<domain>/product/03-roadmap.md` for every
domain except krill's own — for krill, that step instead calls krill's own
`get_product_slice` MCP tool (FR8, whole-product granularity — the same
tool `krill/mcp/tools/slice.go` registers for FR5-FR9, issue #2494) over
the MCP spec surface, ungated by `init` (a read, same as every FR5-FR9/FR11
call — see "`init` and the write gate" above). See
`tools/project-manager/CONVENTIONS.md` "krill's own milestone read is live,
every other domain's is a file" for why this is a narrow, deliberate
exception rather than the start of migrating every domain's read off the
file.

**Resolving krill's own Product id.** FR5-FR9's tools take a surrogate id,
not a name — there is no "find a Product by name" tool in M1. The design
skill resolves krill's own live Product id through its own FR20/C9 pointer
artifact (see "The GitHub pointer artifact" above): the one GitHub issue
titled `Product: krill` whose body carries `krill id \`<uuid>\``. This is
exactly what FR20 exists for — keeping GitHub-side cross-referencing
working once a Product's spec lives in krill's entity model rather than a
file — used here for the read direction instead of the cross-linking
direction FR20's own doc comment (`krill/forge/github.go`) describes. Until
krill's own brief (FR16-FR19, issue #2497) has actually been imported into
a reachable krill instance and a pointer artifact minted for it, this
lookup has nothing to resolve; standing up and importing into that instance
is an operational step, not something this task's code does (`AGENTS.md`:
"Do not patch production environments").

**Known gap: no live per-milestone filter (M3's C13/C28).** `get_product_
slice` returns every current `FeatureSet`/`Feature`/`Requirement`/
`LoadBearingDecision` under krill's Product — it does not, and cannot yet,
filter to just the one milestone being designed. That filter needs
`entity_milestone`/`milestone_ref` (see "The markdown importer and the
delivery-axis association" above), which today is read only by
`krill/render`'s direct `Source` interface (`krill/render/store_source.go`)
against `*krill/store.Store` directly — never over MCP, and never through
FR5-FR9's `slice.Document`, which carries no milestone field at all
(`krill/slice/document.go`). Authoring and exposing that delivery-axis
query is explicitly M3's (C13 authors the cut, C28 exposes status) — M1
"can hold and render milestones that arrived inside an imported document...
holding is not planning" (`krill/product/03-roadmap.md`'s M1 LB6 note).
The design skill therefore treats the live call's result as this
milestone's spec *context* (a superset — every capability/decision in the
product, not a pre-filtered `Delivers:`/`Must not foreclose:` list), and
still consults `krill/product/03-roadmap.md`'s headings (structure only,
not content) to confirm which `M<n>` exists. Architect's own Load-bearing
check (architect.md § Process) still reads the committed file directly for
the authoritative `Must not foreclose` list on every milestone draft, krill
included, so this gap does not leave that check unguarded.

**Failure mode.** An unreachable krill MCP server, or no pointer artifact
to resolve a Product id from, is a **loud, named stop** — "krill's spec MCP
surface is unreachable; cannot read krill's own milestone roadmap live" —
never a silent fallback to reading `krill/product/03-roadmap.md`. A quiet
fallback would leave M1's self-hosting loop unexercised, which is the
failure FR21 exists to prevent (root plan issue #2485).

**Tested at the `slice.Querier` level (LB7).** `krill/conformance/
design_milestone_query_integration_test.go` proves the *data* half of this
read path against a real Postgres holding krill's own imported brief
(#2497's fixture): `GetProductSlice` for krill's own Product surfaces the
same capability descriptions a given milestone's committed `Delivers:`
line names, and a nonexistent/unreachable Product id surfaces a clear,
non-nil, named error rather than an empty or silently-wrong result. Per
LB7 ("M1's MCP tool is a thin wrapper over it, not the thing itself" —
`krill/mcp/tools/slice.go`), testing `slice.Querier` directly exercises the
same code the MCP tool wraps; the MCP wire protocol itself (auth, byte-
identical JSON shape) is already covered by issue #2494's own tests. The
domain-branch decision in `SKILL.md`/`producer.md` itself (krill →
live call, every other domain → file) is verified by diff review, per root
plan issue #2485's own acceptance criteria, not by an automated test —
`tools/project-manager` ships no Bazel targets to run one against.

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
  wires `/project-manager:design --milestone`'s krill-domain read to it
  (issue #2500, see "The design skill's live milestone read" above) —
  still open: a live, MCP-exposed way to filter that read to just one
  milestone's own `Delivers`/`Must not foreclose` entities (M3's C13/C28).
- `krill/slice`'s four granularities each have an as-of assembly twin now
  (issue #2493, see "As-of slice assembly" above) — but no HTTP route
  exposes them yet (`krill/api/handlers/slice.go` still wires only the
  current-row four); that surface, and any MCP tool built over it, remain
  open for a later task.
- `init` (FR3, #2489) and the write-only gate (`api/handlers/session.go`,
  `api/handlers/gate.go`) cover entity creates and LB attach as of #2490,
  and amend as of #2493 (`api/handlers/amend.go`), and pointer-issue
  create as of #2496 (`api/handlers/pointer.go`) — every write path this
  milestone defines is now wired. Import (#2492) is gated too, but as a
  CLI entrypoint checking the session directly against the store rather
  than through this HTTP middleware (see "The markdown importer" above).
- `krill/mcp` (issue #2494) now exists and wraps `krill/slice` directly,
  per LB7 (see "The MCP spec surface" above) — its mcpauth (human) front
  door now has both its verification-side migration (`006_mcpauth_credential`)
  and a mint-side `/authorize`/`/token` surface (`krill/ui`, see
  "krill/ui and the mcpauth front door" above); persona resolution
  (`auth.go`) still always resolves `PersonaSwarmOperator`, unconditionally,
  until C12 lands.
- No auth wired up on `api` — `POST /sessions/init`, every future write
  endpoint, and the FR5-FR9 slice routes all trust caller-asserted
  identity or are unauthenticated (see "`init` and the write gate"
  above); only `krill/mcp` (issue #2494) gets NFR1's two-front-door
  pattern, and only for the read-only spec surface.
- The renderer (`krill/render`, issue #2495) covers FR13-FR15/NFR3 as of
  this task; see "The doc renderer" above. No hook or schedule triggers it
  automatically yet — `bazel run //krill/render/cmd:render` is a manual,
  Swarm-Operator-run step, same shape as the importer.
