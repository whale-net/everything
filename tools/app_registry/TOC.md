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
| [ARCHITECTURE.md](ARCHITECTURE.md) | Before changing the data model, promotability rules, auth split, or writeback mechanism. Contains rejected alternatives and open questions. |
| [PLAN.md](PLAN.md) | Before starting work — current status, what's deployed, deferred/carry-over work, what's next |
| [PLAN-HISTORY.md](PLAN-HISTORY.md) | As-built detail for a specific completed phase (AR-0 … AR-7f) — goal, scope, exit criteria, what shipped. Not meant to be read start to finish; follow a link from PLAN.md's status table. |
| [ENV.md](ENV.md) | Configuring, deploying, or debugging server/migration runtime behavior |
| [TESTING.md](TESTING.md) | Running the registry locally in Tilt for manual integration testing; which checks belong in unit vs Postgres vs Tilt tiers |
| [DEPLOY.md](DEPLOY.md) | Deploying for real — which Keycloak clients and roles to create, server env vars, and where each CI secret goes. Start here when standing the service up in an environment. |
| [OPERATIONS.md](OPERATIONS.md) | Day-2 operations — the release → record → promote → verify → rollback lifecycle, how to spot a silently failed recording, how to check drift, and how to tell whether the registry is actually in use yet. Start here once DEPLOY.md is done and you need to ship something through it. |
| [.mcp.json](.mcp.json) | Claude Code plugin (`app-registry`, see `.claude-plugin/plugin.json`) — three crystaldba `postgres-mcp` servers (`app-registry-pg-{tilt,dev,prod}`) for querying the database directly; see ENV.md "Postgres MCP" |

## Components

| Path | Purpose |
|------|---------|
| [protos/](protos/) | Proto contracts for all four services (`AppRegistry`, `ArtifactRegistry`, `PromotionRegistry`, `EnvironmentRegistry`) |
| [server/](server/) | gRPC server — `app-registry-api` |
| [worker/](worker/) | Temporal worker — gitops writeback (`app-registry-worker`) |
| [cli/](cli/) | Thin gRPC client — `app-registry` |
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
