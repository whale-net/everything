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

`server/repository/postgres/postgres_integration_test.go` (behind
`//go:build integration`) starts a real Postgres container via
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

## Verified

Full chain confirmed on Docker Desktop Kubernetes at AR-1: images build →
namespace → Postgres → migration `Complete` in 4s (8 tables created) → API
starts after it → connects → serves reflection, `SERVING` health, and
`Unimplemented` from real handlers.
