# App Registry — TOC

gRPC service that records published artifacts and tracks per-environment
promotion state.

**Status: AR-M through AR-2c merged to `main`; AR-3 (Promotion) underway.**
Recording works end to end — `ReconcileApps`, `RecordBuild`/`RecordArtifact`
and the chart image lockfile are implemented and verified against real
Postgres. The release workflow calls the CLI's write path after image/chart
pushes, gated behind `APP_REGISTRY_CICD_OPT_IN`. The registry is being
deployed to `dev`. AR-3 is split into a 4-PR stack (auth, environments,
promotions, CLI); environments and promotions stay `Unimplemented` until
AR-3b/3c land.

**See [PLAN.md](PLAN.md) → "Current status" for the branch/PR map,
what is next, and the carry-over items** — start there when picking this up.

## Documents

| File | When to read it |
|------|-----------------|
| [README.md](README.md) | Starting point — what the registry is, core concepts, end-to-end flow diagrams |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Before changing the data model, promotability rules, auth split, or writeback mechanism. Contains rejected alternatives and open questions. |
| [PLAN.md](PLAN.md) | Before starting work — phase definitions (AR-0 … AR-5), scope, and exit criteria |
| `TODO-<PLAN_ID>.md` | Execution tracking for an in-flight phase; created when that phase starts |
| [ENV.md](ENV.md) | Configuring, deploying, or debugging server/migration runtime behavior |
| [TESTING.md](TESTING.md) | Running the registry locally in Tilt for manual integration testing; which checks belong in unit vs Postgres vs Tilt tiers |

## Components

| Path | Purpose |
|------|---------|
| [protos/](protos/) | Proto contracts for all four services (`AppRegistry`, `ArtifactRegistry`, `PromotionRegistry`, `EnvironmentRegistry`) |
| [server/](server/) | gRPC server — `app-registry-api` |
| [worker/](worker/) | Temporal worker — gitops writeback (`app-registry-worker`) |
| [cli/](cli/) | Thin gRPC client — `app-registry` |
| [migrate/](migrate/) | Schema migrations — `app-registry-migration` |
| [Tiltfile](Tiltfile) | Local dev — `tilt up` from `tools/app_registry/` |

## Related

- [`../../libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) — Keycloak setup for the role model enforced in `server/auth`; the reference pattern for service-to-service auth in this repo
- [`../../docs/RELEASE.md`](../../docs/RELEASE.md) — the existing release system this registry indexes
- [`../appmeta/README.md`](../appmeta/README.md) — **shared manifest schema** (`AppManifest`, `DeployUnit`); the registry consumes it rather than defining its own
- [`../bazel/release.bzl`](../bazel/release.bzl) — `release_app` manifests, the source of app identity
- [`../helm/README.md`](../helm/README.md) — chart composition; source of the chart→image lockfile
- [`../release_helper_go/`](../release_helper_go/) — release CLI that will call the registry
- [`../../AGENTS.md`](../../AGENTS.md) — SCD2 conventions used by the `promotion` table
