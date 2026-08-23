# App Registry — TOC

gRPC service that records published artifacts and tracks per-environment
promotion state.

Recording, promotion, writeback, and the AR-7 release-lifecycle artifact
states (`allocated → publishing → published`) all exist server-side; what's
*actually deployed and in use* changes over time and is **not** repeated
here — see [PLAN.md](PLAN.md) → "Current status" for the live branch/PR map,
what's next, and the carry-over items. Start there when picking this up
cold; this file only indexes where things live.

## Documents

| File | When to read it |
|------|-----------------|
| [README.md](README.md) | Starting point — what the registry is, core concepts, end-to-end flow diagrams |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Before changing the data model, promotability rules, auth split, or writeback mechanism. Index + design principles only — the actual sections live one-per-file under [architecture/](architecture/); jump straight to the file you need via ARCHITECTURE.md's table rather than grepping the old monolith. Contains rejected alternatives and open questions. |
| [PLAN.md](PLAN.md) | Before starting work — current status, what's deployed, deferred/carry-over work, what's next |
| [PLAN-HISTORY.md](PLAN-HISTORY.md) | As-built detail for a specific completed phase (AR-0 … AR-7f) — goal, scope, exit criteria, what shipped. Not meant to be read start to finish; follow a link from PLAN.md's status table. |
| [ENV.md](ENV.md) | Configuring, deploying, or debugging server/migration/worker/UI runtime behavior |
| [TESTING.md](TESTING.md) | Running the registry locally in Tilt for manual integration testing; which checks belong in unit vs Postgres vs Tilt tiers |
| [DEPLOY.md](DEPLOY.md) | Deploying for real — which Keycloak clients and roles to create, server env vars, and where each CI secret goes. Start here when standing the service up in an environment. |
| [OPERATIONS.md](OPERATIONS.md) | Day-2 operations — the release → record → promote → verify → rollback lifecycle, how to spot a silently failed recording, how to check drift, and how to tell whether the registry is actually in use yet. Start here once DEPLOY.md is done and you need to ship something through it. |
| [mcp_config.json](mcp_config.json) | AGY & Claude Code plugin (`app-registry`, see `plugin.json` and `.mcp.json`) — three crystaldba `postgres-mcp` servers (`app-registry-pg-{tilt,dev,prod}`) for querying the database directly; see ENV.md "Postgres MCP" |
| [design/USER_STORIES.md](design/USER_STORIES.md) | Designing or reviewing the admin UI — the persona and stories the wireframes answer to |
| [design/RELEASE_PROMOTE_STORIES.md](design/RELEASE_PROMOTE_STORIES.md) | Scoping AR-5+/AR-8+ release-path work — domain-owner stories for image-only/chart-only builds, one image shared across charts, and redundant (no-op digest) builds; marks what's shipped vs. still a gap |
| [design/PRINCIPLES.md](design/PRINCIPLES.md) | Designing or reviewing the admin UI — the guiding principles behind screen and interaction choices |
| [design/CONCEPTS_AUDIT.md](design/CONCEPTS_AUDIT.md) | Before building any wireframe screen for real — which UI concepts have no backing RPC/CLI today, are schema-only, or are out of this API's scope entirely |
| [design/wireframes/](design/wireframes/README.md) | Iterating on the admin UI wireframes themselves (`bazel run //tools/wireframe`) |
| [design/USER_JOURNEYS_2026-08-23.md](design/USER_JOURNEYS_2026-08-23.md) | Prioritizing UI fixes — point-in-time usability findings from 10 simulated personas playtesting the shipped UI live in local Tilt via Playwright. Cross-cutting bugs (nav hijacking, Apps Catalog/app-detail disagreement, drift-vs-sync-status contradiction) ranked by how many independent personas hit them, plus what's working. A dated snapshot, not a living doc — see [design/USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md](design/USER_JOURNEYS_2026-08-23_TRANSCRIPTS.md) for the full per-persona transcripts behind it |

## Components

| Path | Purpose |
|------|---------|
| [protos/](protos/) | Proto contracts for all four services (`AppRegistry`, `ArtifactRegistry`, `PromotionRegistry`, `EnvironmentRegistry`) |
| [server/](server/) | gRPC server — `app-registry-api` |
| [worker/](worker/) | Temporal worker — gitops writeback and release orchestration (`app-registry-worker`); `worker/writeback/` (`WritebackWorkflow`) and `worker/release/` (`ReleaseWorkflow`, #886/#889) share the binary and default task queue |
| [cli/](cli/) | Thin gRPC client — `app-registry` |
| [ui/](ui/README.md) | HTMX admin UI — `app-registry-ui`. `ui/README.md` for structure/styling; env vars live in the main [ENV.md](ENV.md) "UI" section, not a second file |
| [ui/ARCHITECTURE.md](ui/ARCHITECTURE.md) | Before changing the UI's data-access pattern, role gating, or before adding a new deviation from a wireframe — a **real** doc (unlike `manmanv2/ui/README.md`'s dangling link to the same filename). Covers gRPC-only access with the one `htmxauth` session-DB exception, token forwarding, presentation-only role gating, the as-of/SCD2 read pattern, the FR-13/FR-14 UI-layer policies, the FR-19 actor-identity note, and every recorded wireframe deviation (NFR-19) with its reason |
| [migrate/](migrate/) | Schema migrations — `app-registry-migration` |
| [citest/](citest/) | Tests the CI ↔ CLI seam: extracts the real `app-registry ...` command lines from `.github/workflows`/`.github/actions` and validates them against the live cobra tree and an in-process server, in the canonical release ordering. Read this before changing a CLI flag, a workflow's App Registry step, or a composite action — a mismatch between them is exactly what this package exists to catch before merge. |
| [Tiltfile](Tiltfile) | Local dev — `tilt up` from `tools/app_registry/` |

## Related

- [`../../libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) — Keycloak setup for the role model enforced in `server/auth`; the reference pattern for service-to-service auth in this repo
- [`../../docs/RELEASE.md`](../../docs/RELEASE.md) — the existing release system this registry indexes
- [`../appmeta/README.md`](../appmeta/README.md) — **shared manifest schema** (`AppManifest`, `DeployUnit`); the registry consumes it rather than defining its own
- [`../bazel/release.bzl`](../bazel/release.bzl) — `release_app` manifests, the source of app identity
- [`../helm/README.md`](../helm/README.md) — chart composition; source of the chart→image lockfile
- [`../release_helper_go/`](../release_helper_go/) — release CLI that will call the registry
- [`../../AGENTS.md`](../../AGENTS.md) — SCD2 conventions used by the `promotion` table
