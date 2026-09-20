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

## Live environment testing (NFR17)

These tests cannot be produced broker-free and verify properties of the
deployed system (multi-replica fan-out and real browser paths) that integration
tests cannot observe. They require a Tilt cluster with multi-replica deployments,
kubectl access, and (for NFR17b) a real browser session.

### NFR17(a) — Per-replica fan-out against a live broker

**Objective:** Verify that ONE event published for a release run or a
promotion reaches SSE subscribers attached to **every** `app-registry-ui`
replica, not one at random. This is a broker property (server-named,
non-durable, auto-delete queue per replica — `libs/go/htmxsse`'s `Hub`) and
cannot be asserted broker-free; see #1138's NFR17(a) and #1699's FR17/NFR1.

**Do not go through the Service.** One Service in front of two pods gives no
control over which pod a subscriber lands on, degrading the criterion into
"connect twice and hope." Every subscriber below is a `kubectl port-forward`
straight to one pod.

**Committed tool:** the procedure below is driven by
`//tools/app_registry/scripts/verify_multi_replica_sse` (source:
`tools/app_registry/scripts/verify_multi_replica_sse/main.go`). It seeds the
minimum real `release_run`/`release_run_target` and
`app`/`build`/`artifact`/`promotion` rows needed for both status pages to
render, opens one SSE subscriber per pod for **both** topic families
(`release_run.<id>` and `promotion.<id>` — the same Hub and exchange, so
this is one extra subscriber pair over covering promotions alone), hand-
publishes one event per family, and asserts every subscriber received *a*
push (never an exact count — duplicate/bursty publishes are normal and
harmless, since the fragment re-reads current state at delivery).

**Prerequisites:**
- kubectl configured for the local cluster's context (`kind-everything` for
  this repo's dev container; `docker-desktop` if you're on Docker Desktop
  Kubernetes)
- `tilt`, `kubectl`

**Procedure:**

1. **Bring up `app-registry` at 2 UI replicas.** `APP_REGISTRY_UI_REPLICAS`
   controls the `app-registry-ui` Deployment's `replicas` (Tiltfile; default
   1, unchanged for ordinary local dev):
   ```bash
   cd tools/app_registry
   APP_REGISTRY_UI_REPLICAS=2 tilt up --stream=true
   ```
   Wait for both pods Ready:
   ```bash
   kubectl wait --for=condition=Ready pod -l app=app-registry-ui \
     -n app-registry-local-dev --timeout=120s
   kubectl get pods -n app-registry-local-dev -l app=app-registry-ui
   # expect 2 pods, READY 1/1
   ```

2. **Port-forward each pod to its own local port** (never the Service):
   ```bash
   POD1=$(kubectl get pods -n app-registry-local-dev -l app=app-registry-ui -o jsonpath='{.items[0].metadata.name}')
   POD2=$(kubectl get pods -n app-registry-local-dev -l app=app-registry-ui -o jsonpath='{.items[1].metadata.name}')
   kubectl port-forward -n app-registry-local-dev pod/$POD1 8000:8000 &
   kubectl port-forward -n app-registry-local-dev pod/$POD2 8001:8000 &
   ```
   Tilt's own `setup_postgres`/`setup_rabbitmq` helpers also forward Postgres
   to `localhost:5432` and RabbitMQ to `localhost:5672` by default. **If
   another domain's Tilt session already holds those ports** (a shared dev
   box running more than one domain at once — check with
   `ss -tln | grep -E ':5432|:5672'`), forward Postgres/RabbitMQ from their
   pods directly, to different local ports, and pass those via the tool's
   `--pg-url`/`--rabbitmq-url` flags in step 3:
   ```bash
   kubectl port-forward -n app-registry-local-dev pod/postgres-dev-0 25432:5432 &
   kubectl port-forward -n app-registry-local-dev pod/rabbitmq-dev-0 25672:5672 &
   ```

3. **Run the verification tool:**
   ```bash
   bazel run //tools/app_registry/scripts/verify_multi_replica_sse:verify_multi_replica_sse -- \
     --pg-url="postgres://postgres:password@localhost:25432/app_registry?sslmode=disable" \
     --rabbitmq-url="amqp://rabbit:password@localhost:25672/app-registry-dev" \
     --pod1-addr="http://localhost:8000" \
     --pod2-addr="http://localhost:8001"
   ```
   Omit `--pg-url`/`--rabbitmq-url` (or point them at `localhost:5432`/
   `localhost:5672`) if Tilt's own forwards are free to use.

   **Expected output on pass:**
   ```
   === NFR17(a) multi-replica SSE fan-out result ===
     release-run/pod1 topic=release_run.<id> RECEIVED
     release-run/pod2 topic=release_run.<id> RECEIVED
     promotion/pod1   topic=promotion.<id>   RECEIVED
     promotion/pod2   topic=promotion.<id>   RECEIVED

   PASS: both replicas' subscribers received a push for their own hand-published event.
   ```
   Exit code 0 on pass; non-zero (via `log.Fatalf`) on any subscriber missing
   its push.

   **Failure signature:** one pod's subscriber(s) read `RECEIVED`, the
   other's read `MISSING (timed out waiting for post-publish frame)` — a
   shared/durable queue (or accidentally going through the Service instead
   of per-pod port-forwards) produces exactly this asymmetry, one replica
   sees the event and the other does not. If **every** subscriber reads
   `MISSING` on the *first* run against freshly-started pods, this is most
   likely `htmxsse.Hub`'s lazy broker attach (triggered by that pod's
   first-ever SSE subscriber, asynchronous from there) racing the tool's
   own first publish — the tool already re-publishes every 2s for the
   duration of `--push-timeout` (15s default) to absorb exactly this, so a
   same-run retry succeeding is expected; only a `MISSING` that persists for
   the full `--push-timeout` on a warm pod (i.e., a second run against the
   same still-running pods) indicates an actual fan-out failure.

4. **Record the run's evidence** (pod names, ports, both topic IDs, and the
   tool's RECEIVED/MISSING table) in a comment on the tracking issue.

**Cleanup:**

```bash
# Kill the port-forwards (foreground jobs started with `&` above)
kill %1 %2 %3 %4 2>/dev/null

# Either scale back to 1 replica for continued local dev on this checkout...
kubectl scale deployment/app-registry-ui -n app-registry-local-dev --replicas=1
# ...or tear the whole thing down if this Tilt session was only for this check
tilt down
```

---

### NFR17(b) — Real browser path through ingress and Service (#1699 FR18, closes #1138 NFR17(b))

> This section previously described the `/promotions/{id}` page and a
> generic `~30s` heartbeat, written before #1699's FR18 rework moved live
> updates onto the release-run page. It has been corrected below to match
> the shipped routes, topic, and env var. It was never executed against a
> real deployed environment — see [Execution status](#execution-status)
> at the end of this section before relying on it as evidence NFR17(b) has
> passed.

**Objective — all three parts must hold (#1138's own bar, not merely one
successful push):**

1. A real browser on a deployed environment, reaching `/releases/{release_run_id}`
   through the **ingress and the Service** (not a port-forward), receives a
   pushed update — the Targets table and/or aggregate summary line change
   without a reload.
2. The `Last-Event-ID` request header round-trips intact through that ingress
   on reconnect (no truncation).
3. The connection survives at least one full heartbeat interval without the
   live/not-live indicator ever flipping to not-live.

**Do not count events** — duplicate/bursty publishes are normal and
harmless (the fragment re-reads current state at delivery); assert *a push
was observed*, never an exact count.

**Prerequisites:**
- A deployed environment (dev/stage/prod) running `app-registry-ui` behind its
  real ingress — **not** `tilt up`/`tilt ci`'s port-forwarded local dev
  cluster, which bypasses the ingress entirely and cannot exercise this
  criterion. As of this writing no such environment exists yet for
  app-registry outside local Tilt (see [OPERATIONS.md "What actually deploys
  anything, today?"](OPERATIONS.md#what-actually-deploys-anything-today) —
  provisioning one is out of this task's scope; report it rather than
  standing infrastructure up here if it's still true when you run this).
- A real browser (Chrome, Firefox, Safari) with DevTools, on a machine that
  can reach that environment's ingress host.
- `grpcurl` or `rabbitmqadmin`/broker access to that environment, to trigger a
  state change by hand if you don't want to run a real release.
- `APP_REGISTRY_SSE_HEARTBEAT_INTERVAL` in effect for that environment (default
  `5s` — see [ENV.md](ENV.md), **not** `libs/go/htmxsse`'s 30s library
  default). Record whatever value that deployment actually sets.

**Procedure:**

1. **Identify a `release_run_id` to watch**, either from a real release run
   against that environment, or any existing id — a hand-published event
   doesn't require the run to actually exist in Postgres, since the fragment
   handler re-reads current state at delivery time and the page itself
   already handles a run in any state.

2. **Open the page in a real browser** at
   `https://<ui-ingress-host>/releases/<release_run_id>` (the actual
   `ingress_host` for that environment, e.g. as set in DEPLOY.md's redirect-URI
   table — not `localhost`). Log in if the deployment runs `GRPC_AUTH_MODE=oidc`.

3. **Open DevTools → Network, filter to `sse`** (or search for
   `/releases/<id>/status/sse`). Confirm:
   - The request's `Content-Type` in the response headers is
     `text/event-stream` and stays open (status "pending"/no `Content-Length`).
   - The page shows the `#release-live-status` badge as "Live" (FR14; inspect
     via DevTools Elements if the badge text isn't obviously visible).

4. **Trigger a state change** — either run a real release against this
   environment, or hand-publish on the shared exchange:
   ```bash
   # Exchange: app-registry.htmxsse (tools/app_registry/events/events.go)
   # Routing key: release_run.<release_run_id> (TopicForReleaseRun)
   rabbitmqadmin publish exchange=app-registry.htmxsse \
     routing_key=release_run.<release_run_id> \
     payload='{}'
   ```
   The payload body is irrelevant — the fragment handler re-reads current
   state from Postgres on delivery, it does not deserialize the event body.

5. **Verify part 1 (push observed):** within a few seconds, the Targets
   table and/or aggregate summary visibly update with **no reload** — confirm
   via the Network tab that no new top-level document request fired, only an
   SSE `data:` frame. **Fail signature:** nothing changes until you reload
   the page — either the ingress buffered/dropped the stream, or the publish
   never reached the hub.

6. **Verify part 2 (`Last-Event-ID` round-trip):** force a reconnect (e.g.
   DevTools → Network → right-click the SSE request → close it, or toggle
   offline/online, or reload the page once so the browser's `EventSource`
   reconnects using its last received `id:`). Inspect the **new** SSE
   request's **Request Headers** for `Last-Event-ID` and copy its exact value.
   - **Pass signature:** reconnecting on an *unchanged* run yields a
     **keepalive** (no visible swap) — this is only possible if the baseline
     the server parsed from the header matches what it last sent, i.e. the
     header round-tripped intact.
   - **Fail signature:** the page **always swaps on reconnect**, even when
     nothing changed. `libs/go/htmxsse`'s `parseBaseline` fails safe toward
     swapping on any missing/malformed baseline (deliberately, so a
     truncated header never repeats a genuinely stale one) — so this
     "always swaps" behavior, not an error, is what ingress-level header
     truncation looks like. If you see it, capture the exact `Last-Event-ID`
     value from DevTools alongside what the last `id:` field actually was in
     the SSE stream (visible in the EventStream tab) for the comparison.

7. **Verify part 3 (heartbeat stability):** leave the tab open and observe
   **several** heartbeat intervals (the deployment's
   `APP_REGISTRY_SSE_HEARTBEAT_INTERVAL`, default `5s` — not one tick).
   - **Pass signature:** `#release-live-status` stays "Live" throughout; the
     EventStream tab in DevTools shows `id:`-only or comment/keepalive frames
     arriving on schedule.
   - **Fail signature:** the indicator flips to not-live and
     `#release-reload-container` appears (FR15) despite the server process
     never having restarted — this is the graceful degraded state #1138
     anticipates, but its appearance here means the ingress idle-timed-out or
     buffered the connection.

8. **Record results** as a comment on #1708 (not only in this file):
   ```
   - Date/time of execution:
   - Environment (dev/stage/prod) and ingress host:
   - Browser + version:
   - release_run_id watched, and how the state change was triggered (real
     release vs. hand-published event):
   - Part 1 — push observed without reload: pass/fail, what visibly changed
   - Part 2 — Last-Event-ID value on reconnect, and whether the unchanged-run
     reconnect produced a keepalive (pass) or an unconditional swap (fail):
   - Part 3 — heartbeat interval in effect, how many intervals observed, and
     whether the indicator ever flipped to not-live:
   - Notes:
   ```

**Cleanup:** none required — this procedure makes no changes to the
deployed environment (the hand-published event has no persisted side
effect beyond RabbitMQ's own message lifecycle).

#### Execution status

Not yet executed. As of this task's most recent pass, `kubectl config
current-context` in every environment available to run it resolved to a
local-only cluster (this repo's shared local dev cluster, or Docker
Desktop) with no ingress controller and no `app-registry-ui` deployment —
and no real browser tooling was available in the automated session that
authored this revision. Confirm both of OPERATIONS.md's "What actually
deploys anything, today?" section and the presence of a reachable
`<ui-ingress-host>` before attempting this procedure for real; if neither
exists yet, this criterion cannot be executed and should be reported back
rather than simulated against local Tilt (which is exactly the port-forward
path #1706 already covers and this section exists to *not* duplicate).

---

**Why these checks matter:**

- **NFR17(a)** verifies the core SSE fan-out design: a shared RabbitMQ exchange
  with per-pod server-named queues ensures every connected subscriber gets every
  event, not a random subset. This is the property that makes SSE viable for a
  multi-replica UI deployment.

- **NFR17(b)** verifies the operator's actual deployment path: a real browser
  through a real ingress is the only configuration they will ever use. SSE is
  subject to proxy buffering and idle timeouts that don't show up in direct
  connections; this test catches those failure modes before production.

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
