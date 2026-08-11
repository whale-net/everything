# App Registry — TOC

gRPC service that records published artifacts and tracks per-environment
promotion state.

**Status: AR-M through AR-2c merged to `main`; AR-3 (Promotion) and AR-4
(Writeback) implemented, not yet merged.** Recording works end to end —
`ReconcileApps`, `RecordBuild`/`RecordArtifact` and the chart image
lockfile are implemented and verified against real Postgres. The release
workflow calls the CLI's write path after image/chart pushes, gated behind
`APP_REGISTRY_CICD_OPT_IN`. The registry is being deployed to `dev`. AR-3
was split into a 4-PR stack (auth, environments, promotions, CLI), all now
done: auth (AR-3a), `EnvironmentRegistry` (AR-3b), and `PromotionRegistry`
(AR-3c) are verified against real Postgres; AR-3d filled in the CLI's
`promote`/`rollback`/`status`/`history`/`diff` commands, added
`.github/workflows/promote.yml` (human-triggered, `environment:`-scoped),
and wired the builder credential into `release.yml`'s recording steps.
AR-4 (split AR-4a/AR-4b) adds a Temporal-backed writeback path: every
`Promote`/`Rollback` now enqueues a `writeback_outbox` row in the same
transaction, and `app-registry-worker` drains it into a `WritebackWorkflow`
that renders environment state to a local path (stub implementation —
publishing to the gitops repo or S3 is out of scope, see PLAN.md's AR-4b
section).

**AR-7 (issue #558) — release lifecycle — is designed but not built.** It
closes the release-vs-reconcile coupling with an artifact
`allocated → publishing → published` lifecycle, an identity/manifest-snapshot
split of `app`, and a run log that makes a re-run a resume. Read
ARCHITECTURE.md's "Release lifecycle (issue #558)" before touching recording,
reconcile, or anything in `release.yml`'s registry steps.

**See [PLAN.md](PLAN.md) → "Current status" for the branch/PR map,
what is next, and the carry-over items** — start there when picking this up.

## Documents

| File | When to read it |
|------|-----------------|
| [README.md](README.md) | Starting point — what the registry is, core concepts, end-to-end flow diagrams |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Before changing the data model, promotability rules, auth split, or writeback mechanism. Contains rejected alternatives and open questions. |
| [PLAN.md](PLAN.md) | Before starting work — phase definitions (AR-0 … AR-5), scope, and exit criteria |
| [ENV.md](ENV.md) | Configuring, deploying, or debugging server/migration runtime behavior |
| [TESTING.md](TESTING.md) | Running the registry locally in Tilt for manual integration testing; which checks belong in unit vs Postgres vs Tilt tiers |
| [DEPLOY.md](DEPLOY.md) | Deploying for real — which Keycloak clients and roles to create, server env vars, and where each CI secret goes. Start here when standing the service up in an environment. |
| [OPERATIONS.md](OPERATIONS.md) | Day-2 operations — the release → record → promote → verify → rollback lifecycle, how to spot a silently failed recording, how to check drift, and how to tell whether the registry is actually in use yet. Start here once DEPLOY.md is done and you need to ship something through it. |

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
