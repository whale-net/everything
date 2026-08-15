# App Registry — Local Integration Testing

Running the registry for real against a local Kubernetes cluster. This is the
only way to exercise things unit tests cannot: that the migration job actually
applies the schema, that the API starts *after* it, and that the two agree
about the database.

For what belongs here versus in a unit test, see
[Test tiers](#test-tiers) at the bottom.

## Prerequisites

- **Docker Desktop with Kubernetes enabled.**
- `kubectl config current-context` must be `docker-desktop`.
- `tilt` (v0.35+), `grpcurl`, `kubectl`.

Tilt refuses to run against any context it does not recognise as local, so a
remote context fails closed rather than deploying. Check before you start:

```bash
kubectl config current-context   # expect: docker-desktop
```

**Run from the primary checkout, not a git worktree.** Tilt resolves the
Bazel workspace and `../tilt/common.tilt` relative to the Tiltfile; agent
worktrees under `.claude/worktrees/` have their own Bazel output base and will
either rebuild everything or misresolve paths.

## Two ways to run

### One-shot check (`tilt ci`)

Builds everything, waits for every resource to become healthy, exits non-zero
if anything fails. Non-interactive — this is the pre-merge gate to run once per
phase.

```bash
cd tools/app_registry
tilt ci --timeout 15m
```

Ends with `SUCCESS. All workloads are healthy.` Resources stay running after it
exits, but **port-forwards do not** — see [Reaching the API](#reaching-the-api).

### Interactive (`tilt up`)

Live-reload loop with port-forwards held open and a UI at `localhost:10350`.
Use this for hands-on poking.

```bash
cd tools/app_registry
tilt up
```

## What comes up

Namespace `app-registry-local-dev`:

| Resource | Purpose |
|---|---|
| `postgres-dev-0` | Postgres, database `app_registry` |
| `app-registry-migration` | Job — applies migrations, must reach `Complete` |
| `app-registry-api` | gRPC API on `50051`, forwarded to **`localhost:50061`** |
| `otel-collector` | Receives traces/logs; app logs surface here |
| `temporal-dev` | Temporal dev server (`temporal server start-dev`) — gRPC on `7233`, Web UI on `8233`, both forwarded |
| `app-registry-worker` | Temporal worker (AR-4b) — drains `writeback_outbox`, runs `WritebackWorkflow`. No forwarded port; depends on migration, the API, and `temporal-dev` |

The API declares `resource_deps` on the migration job, mirroring the ArgoCD
pre-sync-wave ordering in [`migrate/README.md`](migrate/README.md). If the
migration fails, the API never starts — that ordering is itself under test.

## Reaching the API

`tilt up` forwards `50061` for you. After `tilt ci`, forward it yourself:

```bash
kubectl port-forward -n app-registry-local-dev svc/app-registry-api 50061:50051
```

## Smoke checks

All four services registered, plus health and reflection:

```bash
grpcurl -plaintext localhost:50061 list
```

```
appregistry.v1.AppRegistry
appregistry.v1.ArtifactRegistry
appregistry.v1.EnvironmentRegistry
appregistry.v1.PromotionRegistry
grpc.health.v1.Health
grpc.reflection.v1.ServerReflection
```

Health:

```bash
grpcurl -plaintext localhost:50061 grpc.health.v1.Health/Check
# {"status": "SERVING"}
```

A real RPC — until a phase implements it, `Unimplemented` **with the handler's
own message** is the correct answer:

```bash
grpcurl -plaintext -d '{}' localhost:50061 appregistry.v1.AppRegistry/ListApps
# Code: Unimplemented   Message: ListApps not implemented
```

> A bare `Unimplemented` with message `unknown service ...` means the service
> was never registered — a different bug entirely. `server/main_test.go`
> asserts on the message text for exactly this reason.

## Temporal (AR-4a)

AR-4a adds `libs/go/temporal` (client/worker bootstrap) and a Temporal dev
server to Tilt. This section is for exercising the dev server and the
library directly; see "Writeback worker (AR-4b)" below for the actual
`WritebackWorkflow`.

`tilt up` forwards the gRPC frontend to `localhost:7233` and the Web UI to
`localhost:8233`. Confirm the dev server is reachable:

```bash
temporal operator cluster health --address localhost:7233
# SERVING

open http://localhost:8233   # Web UI
```

`libs/go/temporal`'s own unit tests (config parsing, the logging bridge) run
without a live server:

```bash
bazel test //libs/go/temporal/...
```

To exercise a real client connection, point a small program or
`temporal workflow list --address localhost:7233` at the forwarded port —
`ConfigFromEnv()`'s `TEMPORAL_HOST` default (`localhost:7233`) matches the
Tilt port-forward, so no env var is needed when running against `tilt up`
locally.

Disable the dev server with `ENABLE_TEMPORAL=false` if you don't need it —
this also disables `app-registry-worker` (see below), which has nothing to
poll without it.

## Writeback worker (AR-4b)

`app-registry-worker` drains `writeback_outbox` and runs one
`WritebackWorkflow` per row, rendering environment state to a local path
inside its own container (`WRITEBACK_OUTPUT_DIR`, see [ENV.md](ENV.md)) —
see [`worker/README.md`](worker/README.md) and
[ARCHITECTURE.md](ARCHITECTURE.md) "Writeback: outbox -> Temporal" for the
mechanism.

End-to-end smoke check under `tilt up`:

```bash
# Promote something (needs a recorded app/artifact and a dev environment
# first -- see worker/README.md for a full from-scratch script).
grpcurl -plaintext -d '{
  "environment_key": "dev", "owner_full_name": "<domain>-<name>",
  "kind": "ARTIFACT_KIND_IMAGE", "version": "v1.0.0",
  "idempotency_key": "smoke-1"
}' localhost:50061 appregistry.v1.PromotionRegistry/Promote

# Confirm the outbox row was written in the same transaction and drained:
kubectl exec -n app-registry-local-dev postgres-dev-0 -- \
  psql -U postgres -d app_registry -c \
  "select outbox_id, status, workflow_id from writeback_outbox order by created_at desc limit 5;"
# status should reach 'done' within one WRITEBACK_POLL_INTERVAL (default 5s)

# Confirm the workflow ran in Temporal's Web UI (localhost:8233) under
# workflow id = the promotion id from the Promote response above, and that
# it rendered the same state GetEnvironmentState reports:
grpcurl -plaintext -d '{"environment_key": "dev"}' \
  localhost:50061 appregistry.v1.PromotionRegistry/GetEnvironmentState
kubectl exec -n app-registry-local-dev deploy/app-registry-worker -- \
  cat /tmp/app-registry-writeback/dev.json
```

`worker/writeback`'s and `worker/outbox`'s own unit tests run without a live
Temporal server, using the SDK's `testsuite` (workflow logic) and a plain
fake `repository.WritebackRepository` (drain logic):

```bash
bazel test //tools/app_registry/worker/...
```

### Verifying "killed mid-run" (AR-4b's exit criterion)

This was verified manually, outside `bazel test` and outside Tilt (no k8s
overhead needed to exercise the mechanism): real Postgres + a real
`temporal server start-dev` in Docker, the real `app-registry-api` and
`app-registry-worker` binaries via `bazel run`, `grpcurl` for the promote
call. A promotion was made, the worker process was made to exit (`os.Exit`,
simulating a kill) immediately after it logged that it had started
`WritebackWorkflow` but *before* it called `MarkDone` — the exact window
AR-4b's outbox-claim design exists to survive. The outbox row was confirmed
stuck `'claimed'` with the dead worker's id. A second worker process was
then started with no special flags: after `WRITEBACK_CLAIM_STALE_AFTER`
elapsed, it reclaimed the row, called `ExecuteWorkflow` for the same
workflow id (Temporal transparently attached to the still-running
execution rather than erroring), and the *same* run id from the first
attempt completed and published — the outbox row reached `'done'` and
`/tmp/.../dev.json` updated to the promoted artifact's digest, with no
duplicate publish. The temporary `os.Exit` hook used to force the crash
point deterministically was removed from `worker/outbox/drain.go` again
immediately after; it is not part of the shipped code.

## Inspecting the database

```bash
kubectl exec -n app-registry-local-dev postgres-dev-0 -- \
  psql -U postgres -d app_registry -c "\dt"
```

Confirm the migration job succeeded:

```bash
kubectl get job -n app-registry-local-dev app-registry-migration
# COMPLETIONS should read 1/1
```

Migration logs:

```bash
kubectl logs -n app-registry-local-dev job/app-registry-migration
```

## Teardown

```bash
cd tools/app_registry && tilt down
```

Postgres is a StatefulSet — delete its PVC if you want a genuinely clean
schema:

```bash
kubectl delete pvc -n app-registry-local-dev --all
```

## Test tiers

Where a given check belongs:

| Tier | Runs in | Catches |
|---|---|---|
| **Unit / fakes** | `bazel test //tools/...` | Business logic. Cannot catch a wrong query, an index that doesn't exist, or a constraint that never fires. |
| **Postgres integration** | `manual`-tagged target | Real SQL: constraint enforcement, transaction semantics, unique-index guarantees. |
| **Tilt (this doc)** | Manual, per phase | Deployment reality: chart/manifests apply, migration ordering, service wiring, config plumbing. |

Two invariants are **schema** guarantees and therefore untestable with fakes —
they need the Postgres tier, not this one:

- `artifact_version_idx` — the concurrent-allocation guard AR-5 relies on to
  replace the CI concurrency group.
- `promotion_current_idx` (partial unique, `WHERE valid_to IS NULL`) — what
  makes double-promotion structurally impossible. Needed before AR-3 ships.

### Running the Postgres integration tier (AR-2d)

`server/repository/postgres/postgres_integration_*_test.go` (behind
`//go:build integration`; split by which repository file each group of tests
exercises -- see `postgres_integration_helpers_test.go`'s doc comment)
starts a real Postgres container via
`libs/go/dbtest`, applies the real migrations from `migrate/schema` (the same
embedded SQL `app-registry-migration` runs — not hand-written DDL), and
exercises transaction-abort rollback, idempotency-key replay, real
unique-index enforcement, and the `ResolveArtifact` chart→image join. See
`libs/go/dbtest/README.md` for the general pattern and its rough edges.

Requires a working Docker daemon. Run it explicitly — it is `manual`-tagged
and excluded from `bazel test //...`:

```bash
bazel test //tools/app_registry/server/repository/postgres:postgres_integration_test \
  --test_output=all
```

**CI runs this** in the `Test Database Integration` job
(`.github/workflows/ci.yml`), which discovers dbtest-backed targets by query —
a new one needs no CI change. Run it locally before pushing anyway: it is the
only automated check that exercises the pgx layer, and because it is
`manual`-tagged, `bazel test //...` will not tell you it is broken.

## Verified

Full chain confirmed on Docker Desktop Kubernetes at AR-1: images build →
namespace → Postgres → migration `Complete` in 4s (8 tables created) → API
starts after it → connects → serves reflection, `SERVING` health, and
`Unimplemented` from real handlers.

AR-4b's writeback path was verified end to end (real Postgres + real
Temporal + the real `app-registry-api`/`app-registry-worker` binaries, not
Tilt/k8s — see "Verifying 'killed mid-run'" above): a `Promote` call
enqueues exactly one `writeback_outbox` row in the same transaction; the
worker drains it into a `WritebackWorkflow` whose rendered output matches
`GetEnvironmentState`; killing the worker between "workflow started" and
"outbox marked done" leaves the row `'claimed'`, and a second worker process
reclaims it after the staleness window and drives the *same* workflow run to
completion with no duplicate publish. Not verified: the real gitops/S3
publish path (explicitly out of scope for AR-4b) and a true concurrent
two-workers-racing-one-claim scenario (the `FOR UPDATE SKIP LOCKED` claim
query's correctness is asserted against real Postgres in
`postgres_integration_promotion_test.go`, not exercised by two live worker
processes).
