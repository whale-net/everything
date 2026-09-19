# krill

krill is the spec-of-record and work-tracking substrate for agent swarms —
see [`PRODUCT.md`](PRODUCT.md) for the vision, personas, load-bearing
decisions, and milestone roadmap.

This task (issue #2487) stands krill up as a Bazel domain following the
`whagent_net` / `audience_score_system` template: a `migrate` job, an `api`
binary skeleton, and the `scope` table (LB1) every other table in this
milestone hangs off. No spec entities exist yet — that is later M1 work.

## Binaries

| Binary | Target | Type | Description |
|--------|--------|------|-------------|
| `migrate` | `//krill/migrate` | job | Applies `krill/migrate/schema/migrations` and seeds the one `scope` row with this repo's forge coordinates (LB1, NFR2). |
| `api` | `//krill/api` | external-api | HTTP server; `/healthz` (a live DB ping), `POST /sessions/init` (FR3's `init` primitive, issue #2489), the M1 entity write API (FR1/FR2/FR4, issue #2490), the FR5-FR9 scoped-slice query surface (`GET /slices/{feature-sets,features,requirements,products}/{id}`, issue #2491), the pointer-artifact create endpoint (`POST /pointer-artifacts`, FR20, issue #2496), and (M3, issues #2683-#2689) the delivery-axis surface -- milestone/milepebble authoring, status, shipment, re-cut, backlog, and abandon. See "Delivery-axis endpoints" below. |
| `import` | `//krill/importer/cmd` | CLI (not deployed) | The one-way markdown importer (FR16, FR17, issue #2492): parses a `PRODUCT.md` + `product/*.md` doc set into `krill/store`'s spec entities and prints the entity-id report. Gated on a valid `init` session, same as every other write path. Records a one-time, one-way `import_completion` marker after a successful run and refuses a second import for the same path before parsing (FR12, NFR3, issue #2548). Run with `bazel run //krill/importer/cmd:import -- --path <dir> --session-id <uuid> --source-revision <sha>`. See `ARCHITECTURE.md` "The markdown importer and the delivery-axis association". |
| `mcp` | `//krill/mcp` | external-api | krill's MCP surface: the FR5-FR9 scoped-slice query over MCP at `/mcp/spec`, and (issue #2547) the FR1-FR10 design-session/mediated-intake surface plus (M3, issues #2683-#2689) the delivery-axis tool set at `/mcp/design`, both behind the mcpauth (human) + whagent-net (agent) two-front-door auth pattern. See "MCP spec surface", "Design-session MCP surface", and "Delivery-axis endpoints" below. |
| `ui` | `//krill/ui` | external-api | Barebones Keycloak sign-in shell: gives mcpauth's `/authorize` endpoint (mounted here) a `SignInURL` to redirect a not-yet-signed-in caller to, so the human front door above can actually mint a credential end to end. No session list, no spec browsing -- a real web UI is deferred (`PRODUCT.md`'s C19, "Later"). See "The mcpauth sign-in shell" below. |

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | Live DB connectivity check. Never gated. |
| `POST /sessions/init` | Mints a krill-native session id (FR3). Body: `{"scope_id": "<uuid>", "acting": {"iss", "sub", "kind"}, "on_behalf_of": {"iss", "sub", "kind"}, "whagent_session_id": "<optional string>"}`; `kind` is `human` or `service`. Returns `{"session_id": "<uuid>"}`. Every write endpoint below (and every write endpoint added by a later M1 task -- #2493/#2496) requires the resulting id on an `X-Krill-Session-Id` header (`api/handlers/gate.go`'s `RequireSession`) — see `ARCHITECTURE.md` "`init` and the write gate" for why `init` itself takes the caller's identity fields as-is rather than verifying a bearer credential. The importer (`//krill/importer/cmd`, issue #2492) is gated the same way but takes the resulting id as a `--session-id` flag, since it is a CLI, not an HTTP write endpoint. |
| `POST /products` | Creates a Product (FR1). Body: `{"name", "vision"}`. No parent -- top of the spec chain. Gated. Returns `{"id": "<uuid>"}` (the surrogate id, LB2 -- never a display number). |
| `POST /feature-sets` | Creates a FeatureSet under a Product (FR2). Body: `{"product_id", "name", "description"?}`. Gated. Returns `{"id": "<uuid>"}`. |
| `POST /features` | Creates a Feature under a FeatureSet (FR2). Body: `{"feature_set_id", "name", "description"?}`. Gated. Returns `{"id": "<uuid>"}`. |
| `POST /requirements` | Creates an FR or NFR under a Feature (FR2). Body: `{"feature_id", "kind": "FR"\|"NFR", "name", "body"?}`. Gated. Returns `{"id": "<uuid>"}`. |
| `POST /load-bearing-decisions` | Attaches a Load-Bearing Decision to the FeatureSet it constrains (FR4) — not a Product, not a Feature (C2). Body: `{"feature_set_id", "name", "body"?}`. Gated. Returns `{"id": "<uuid>"}`. |
| `POST /pointer-artifacts` | Creates krill's one thin GitHub pointer issue for a Product (FR20, C9) — so existing PR/commit/conversation cross-linking keeps working now that the spec lives in krill instead of a file. Body: `{"product_id"}`. Gated. Creates the issue via `//krill/forge.GitHubClient` (`KRILL_GITHUB_TOKEN`, see `ENV.md`) against the caller's scope's `repo_full_name`, records it as a `pointer_artifact` row (both LB4 subjects always recorded), and mirrors the issue number onto `scope.pointer_issue_number` (LB1). Rejects a product that already has one with 409 (`pointer_artifact_product_idx`). Returns `{"id": "<uuid>", "issue_number": <int>, "issue_url": "<string>"}`. Retrievable afterward through `GET /slices/products/{id}`'s `pointer_artifacts` field (FR8). |
| `GET /slices/feature-sets/{id}` | Returns the FR5 scoped slice: a FeatureSet, its Features, their FRs/NFRs, and only the LoadBearingDecisions attached to that FeatureSet. Never gated (read-only). |
| `GET /slices/features/{id}` | Returns the FR6 scoped slice: a Feature and its FRs/NFRs. Never gated. |
| `GET /slices/requirements/{id}` | Returns the FR7 scoped slice: a single FR or NFR by surrogate id. Never gated. |
| `GET /slices/products/{id}` | Returns the FR8 scoped slice: every FeatureSet, Feature, FR, NFR, LoadBearingDecision, and PointerArtifact beneath a Product. Never gated. |

Every gated endpoint above:
- requires `X-Krill-Session-Id` (`api/handlers/gate.go`'s `RequireSession`) — rejects with 401 if missing/unknown;
- takes exactly one parent reference as a request field (never a list) — an unrecognized extra field is rejected with 400 (strict JSON decoding);
- writes `scope_id` from the session's scope (LB1) — never a client-supplied field;
- rejects a nonexistent or cross-scope parent with 400, and a scope-qualified duplicate name with 409 (never a 500 for either).

The four `GET /slices/...` endpoints above are read-only and carry no
`RequireSession` gate (FR3's `init` gate is write-only) — see
`ARCHITECTURE.md` "The scoped-slice query" for the shared `slice.Document`
response shape (FR9) all four return.

## Delivery-axis endpoints (M3, issues #2683-#2689)

M3 adds the delivery axis on top of the spec entities above: milestone
authoring, milepebbles, status history, per-item shipment, re-cut, the
backlog bucket, and abandon. See `ARCHITECTURE.md` "M3's delivery axis,
end to end" for the design; every endpoint here follows the same gating
rules the table above states (`RequireSession` on every write, never on a
read) unless noted otherwise.

| Endpoint | Description |
|----------|-------------|
| `POST /milestones` | Creates a milestone under a Product with an outcome sentence and an optional FR budget (FR1, FR2, issue #2683). Body: `{"product_id", "name", "outcome", "fr_budget"?}`. Gated. Returns `{"id": "<uuid>"}`. |
| `POST /milestones/{id}/fr-budget` | Revises a milestone's FR budget (FR2) — the current value after this call is the only one that reads back; the prior value is not resurrected. Gated. |
| `POST /milestones/{id}/delivers` | Adds a Feature or LoadBearingDecision to a milestone's Delivers set (LB6 — an `entity_milestone` row, never a column on the entity). Idempotent. Gated. |
| `POST /milestones/{id}/must-not-foreclose` | Adds a LoadBearingDecision to a milestone's Must-not-foreclose set, the same association mechanism as Delivers, discriminated by `relation`. Idempotent. Gated. |
| `POST /milestones/{id}/deferrals` | Records one deliberately-deferred item on a milestone, with a required destination (FR1). Gated. |
| `GET /milestones/{id}` | Returns a milestone's authoring fields, Delivers/Must-not-foreclose association sets, and deferrals. Never gated. |
| `POST /milestones/{id}/milepebbles` | Cuts a new milepebble from a milestone (FR3, issue #2684). Gated. Returns `{"id": "<uuid>"}`. |
| `POST /milepebbles/{id}/delivers` | Adds an entity to a milepebble's Delivers set — rejected if the entity is not already in the parent milestone's own Delivers set (FR3's subset invariant). Gated. |
| `POST /milepebbles/{id}/discovered-scope` | Lands mid-milestone discovery as a real Feature or Requirement row, associated to the milepebble and, in the same transaction, to its parent milestone's Delivers set (FR4). Gated. |
| `GET /milepebbles/{id}` | Returns a milepebble's own fields and Delivers set. Never gated. |
| `GET /milestones/{id}/milepebbles` | Lists a milestone's milepebbles in position order. Never gated. |
| `POST /milestones/{id}/status` | Appends a status transition for a milestone or milepebble (FR8, FR9, issue #2685) — a re-affirmation of the current status still appends a new row. Gated. |
| `GET /milestones/{id}/status` | Returns the current (latest) status, or "not started" when no transition has ever been recorded. Never gated. |
| `GET /milestones/{id}/status/history` | Returns every status transition in chronological order, each with its actor and timestamp (FR12). Never gated. |
| `POST /milestones/{id}/shipped` | Records that a specific delivered entity has shipped as part of this container (FR10, issue #2686). Gated. |
| `GET /milestones/{id}/delivery` | Returns the shipped/unshipped breakdown of a container's Delivers set, as typed entities (FR10). Never gated. |
| `POST /delivery/move` | Re-cuts not-yet-shipped scope between milestones, milepebbles, or the backlog bucket (FR5, issue #2687) — refuses (writing nothing) if any entity is already shipped in its from-container. Gated. |
| `GET /products/{id}/backlog` | Returns a product's backlog bucket contents, as typed entities. Never gated. |
| `POST /milestones/{id}/abandon` | Marks a milestone or milepebble abandoned, sweeping its not-yet-shipped scope into the backlog bucket in one transaction; cascades to every live milepebble when the target is a milestone (FR6, issue #2688). Not reversible — there is no un-abandon endpoint. Gated. |
| `GET /products/{id}/delivery` | Returns every milestone and milepebble under a product, filtered by status (FR11, issue #2689) — an empty filter means "all". Never gated. |

The MCP surface below mirrors every endpoint above one-to-one (same
persona/session rules as the design-session tools) — see "Design-session
MCP surface" below for the mount and auth pattern these delivery-axis
tools share.

| Tool | Wraps |
|------|-------|
| `create_milestone`, `set_fr_budget`, `add_delivers`, `add_must_not_foreclose`, `add_deferral`, `get_milestone` | `MilestoneAuthoringStore` (FR1, FR2, LB6) |
| `create_milepebble`, `add_milepebble_scope`, `add_discovered_scope`, `list_milepebbles` | `MilestoneAuthoringStore` (FR3, FR4) |
| `set_milestone_status`, `get_milestone_status`, `get_milestone_status_history` | `MilestoneStatusEventStore` (FR8, FR9, FR12) |
| `mark_delivered_item_shipped`, `get_delivery_breakdown` | `DeliveryShipmentStore` (FR10) |
| `move_delivery_scope`, `get_backlog` | `RecutStore` (FR5) |
| `abandon_milestone` | `AbandonStore` (FR6) |
| `list_product_delivery` | `slice.Querier.ListProductDelivery` (FR11) |

## Local development

```sh
# Build everything in the domain
bazel build //krill/...

# Run the migrate job directly against a local Postgres
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/migrate

# Run api
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/api
curl http://localhost:8080/healthz

# Read a scoped spec slice (FR5-FR9) -- same call shape for all four
# granularities, only the path segment and id change:
curl http://localhost:8080/slices/products/<product-id>

# Mint a session, then import a product's doc set (issue #2492)
SESSION_ID=$(curl -s -X POST http://localhost:8080/sessions/init \
  -d '{"scope_id":"<scope-uuid>","acting":{"iss":"local","sub":"me","kind":"human"},"on_behalf_of":{"iss":"local","sub":"me","kind":"human"}}' \
  | jq -r .session_id)
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/importer/cmd:import -- --path krill --session-id "$SESSION_ID" \
  --source-revision "$(git rev-parse HEAD)"
```

`--source-revision` is required (FR12, NFR3, issue #2548): the commit SHA
`--path` was imported from, recorded on the `import_completion` row for
the audit trail only -- krill does not shell out to git itself, so the
caller supplies it. A second `import` run for a `--path` already recorded
as complete for the resolved session's scope refuses before parsing
anything; see `ARCHITECTURE.md` "The markdown importer and the
delivery-axis association" for the one-time, one-way guarantee.

Or bring up the whole domain (Postgres + migrate + api) via Tilt:

```sh
cd krill && tilt up
```

See [`ENV.md`](ENV.md) for every environment variable `migrate` and `api`
read, and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the component map and
the `scope` table's design rationale.

## Importing whagent_net's brief (FR11, issue #2549)

whagent_net's product brief (`whagent_net/PRODUCT.md` + `whagent_net/product/*.md`)
already follows the layout `krill/importer` parses. An Operator/Admin runs
the same `import` CLI described above, pointed at `whagent_net` instead of
`krill`:

```sh
SESSION_ID=$(curl -s -X POST http://localhost:8080/sessions/init \
  -d '{"scope_id":"<scope-uuid>","acting":{"iss":"local","sub":"me","kind":"human"},"on_behalf_of":{"iss":"local","sub":"me","kind":"human"}}' \
  | jq -r .session_id)
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/importer/cmd:import -- --path whagent_net --session-id "$SESSION_ID" \
  --source-revision "$(git rev-parse HEAD)"
```

This is a one-pass, one-time import (FR12, NFR3, issue #2548): a second run
against the same `--path` for the same session's scope refuses before
parsing anything.

**Expected report shape.** The printed report has two parts:

1. One line per entity created, exactly as the M1 self-import prints
   (`[<kind>] <source id> <name> -> <entity id>`) -- one for every persona,
   load-bearing decision, non-goal, capability, and milestone
   `whagent_net`'s brief defines.
2. A trailing **Coverage** section (FR11): a per-source-file count of
   recognized-but-unmapped items. `0` everywhere is the expected result for
   `whagent_net`'s current brief -- confirmation that nothing was lost in
   the import. A non-zero count fails the command (non-zero exit) unless
   rerun with `--allow-unmapped`, which downgrades it to a logged WARNING
   and proceeds anyway; never mistake a partial import for a complete one
   by rerunning with that flag out of habit.

See `ARCHITECTURE.md` "The markdown importer" for why `whagent_net` is the
first product krill holds that krill did not author.

## MCP spec surface (FR10/NFR1, issue #2494)

`mcp` exposes the FR5-FR9 scoped-slice query over MCP at `/mcp/spec` --
any MCP-capable harness (Claude Code today) reaches it with no
krill-specific harness code. Every tool is a thin wrapper over
`krill/slice`'s query layer (LB7): it returns `slice.Document` unchanged,
never a bespoke per-tool projection.

| Tool | Wraps | Description |
|------|-------|-------------|
| `get_feature_set_slice` | `slice.Querier.GetFeatureSetSlice` (FR5) | A FeatureSet, its Features, their FRs/NFRs, and only the LoadBearingDecisions attached to that FeatureSet. |
| `get_feature_slice` | `slice.Querier.GetFeatureSlice` (FR6) | A Feature and its FRs/NFRs. |
| `get_requirement_slice` | `slice.Querier.GetRequirementSlice` (FR7) | A single FR or NFR by surrogate id alone. |
| `get_product_slice` | `slice.Querier.GetProductSlice` (FR8) | Every FeatureSet, Feature, FR, NFR, and LoadBearingDecision beneath a Product. |

Every tool takes `{"id": "<uuid>"}` -- the surrogate id (LB2) of the
entity to slice from. None is gated by `init`/session (FR3's gate is
write-only); every call still requires an authenticated persona (see
below). No write tool is registered on this endpoint in M1.

**Auth (NFR1)** -- both front doors mounted at `/mcp/spec`, each
independently env-gated (see `ENV.md`), authorized by **persona** (Swarm
Operator / Requirement Contributor / Agent), never individual identity:

- human callers via `//libs/go/mcpauth` (OAuth2-capable) -- resolves to
  `PersonaSwarmOperator` in M1 (see `krill/mcp/server/auth.go`'s doc
  comment for why no second human persona is distinguished yet);
- agent callers via the `//libs/go/whagent` verifier -- resolves to
  `PersonaAgent` unconditionally.

See `ARCHITECTURE.md` "The MCP spec surface" for the full design.

## Design-session MCP surface (FR1-FR10 over MCP, NFR4, issue #2547)

`mcp` also exposes the design-session/mediated-intake HTTP surface above
over MCP, at its own mount, `/mcp/design` -- never on `/mcp/spec`, and
never reachable from it. This is krill's first MCP **write** surface.

| Tool | Kind | Wraps | Persona |
|------|------|-------|---------|
| `open_design_session` | write | `DesignSessionStore.Open` (FR1, FR8) | any resolved persona |
| `append_revision_event` | write | `RevisionEventStore.Append` (FR2-FR4, FR7) | any resolved persona |
| `propose_entities` | write | `MediatedWriteStore.ProposeEntities` (FR9, FR10, NFR2) | **Agent only** |
| `get_design_session` | read | session + ordered revision log (FR2) | any resolved persona |
| `get_design_session_slice` | read | `slice.Querier.GetEntitySetSlice` over the session's id union (FR5) | any resolved persona |
| `list_open_questions` | read | derived open-question view (FR6) | any resolved persona |

Every write tool takes a `krill_session_id` field -- the same id
`POST /sessions/init` mints and the HTTP surface's `X-Krill-Session-Id`
header carries, validated the same way (`api/handlers/gate.go`'s
`RequireSession`) but as an ordinary input field, since an MCP tool call
has no header to carry it. `propose_entities` is the one tool restricted to
the Agent persona (via the whagent-net front door): FR9/FR10's mediated
write requires a producer-role Agent acting on a Requirement Contributor's
behalf, which a human acting for itself can never satisfy. Every response
is one of `krill/api/handlers`' own exported types, or (`get_design_session_slice`)
`slice.Document` itself, unchanged (LB7) -- never a bespoke MCP-only shape.

See `ARCHITECTURE.md` "The design-session MCP surface" for the full design.

## The mcpauth sign-in shell (`ui`)

`ui` mounts mcpauth's OAuth2 authorization-server endpoints (`/authorize`,
`/token`, `/register`, and both discovery metadata documents) and the
Keycloak sign-in flow (`/login`, `/auth/callback`, `/logout`) they redirect
an unresolved caller to. This is what makes the mcpauth (human) front door
on `mcp` actually usable end to end -- before `ui` existed, `/authorize`
had no `SignInURL` configured and any unresolved caller just got a 401
(see `ARCHITECTURE.md` "krill/ui and the mcpauth front door"). The one
authenticated page it serves (`GET /`) is a bare "signed in as ..." shell,
not a real operator UI.

```sh
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  KRILL_UI_PUBLIC_URL=http://localhost:8085 \
  KRILL_MCP_PUBLIC_URL=http://localhost:8084 \
  bazel run //krill/ui
```

See `ENV.md` "`ui` (Keycloak sign-in shell, mcpauth's `/authorize` front
end)" for every variable it reads.

## Claude Code plugin

`plugin/user/` is the Claude Code plugin layout this domain exposes MCP
tools through, mirroring `whagent_net/plugin` / `audience_score_system/plugin`:
`.mcp.json` / `mcp_config.json` register `krill-mcp-tilt` (local Tilt,
`http://localhost:8084/mcp/spec`), `krill-mcp-dev`, and `krill-mcp-prod` for
the FR5-FR9 spec surface, plus (issue #2547) `krill-mcp-design-tilt`
(`http://localhost:8084/mcp/design`), `krill-mcp-design-dev`, and
`krill-mcp-design-prod` for the design-session surface above -- one entry
per mount per environment, since each is its own pre-filtered MCP endpoint.
Registered in `.claude-plugin/marketplace.json` as `krill`.

`plugin/data/` is the companion "-data" plugin, mirroring
`audience_score_system/plugin/data` / `leaflab/plugin/data`: direct
read-restricted crystaldba `postgres-mcp` access to the same `krill`
Postgres database `migrate`/`api`/`mcp` share, one server per environment
(`krill-pg-tilt`, `krill-pg-dev`, `krill-pg-prod` — see `ENV.md` "Postgres
MCP (Claude Code plugin)"). Registered as `krill-data`.
