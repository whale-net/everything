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

**Objective:** Verify that an event published by one UI replica reaches SSE
subscribers attached to **every** replica (not one at random). This tests the
broker's server-named, non-durable, auto-delete queue behavior (FR1/NFR1).

**Prerequisites:**
- Tilt environment running (`tilt up`)
- kubectl configured for `docker-desktop` context
- Two or more pod replicas of `app-registry-ui`
- Real RabbitMQ instance (provided by Tilt's `setup_rabbitmq`)
- AUTH_MODE=none (already set in Tiltfile)

**Procedure:**

1. **Scale the UI deployment to 2+ replicas:**
   ```bash
   kubectl scale deployment/app-registry-ui -n app-registry-local-dev --replicas=2
   kubectl wait --for=condition=Ready pod -l app=app-registry-ui \
     -n app-registry-local-dev --timeout=60s
   ```
   Verify both pods are running:
   ```bash
   kubectl get pods -n app-registry-local-dev -l app=app-registry-ui
   # Should show 2 pods in Ready state
   ```

2. **Set up per-pod port-forwards (one SSE subscriber per pod):**
   
   Open three terminal windows. In the first, forward pod 1 to a local port:
   ```bash
   # Terminal 1: Get the first pod name
   POD1=$(kubectl get pods -n app-registry-local-dev -l app=app-registry-ui \
     -o jsonpath='{.items[0].metadata.name}')
   echo "Pod 1: $POD1"
   
   # Forward pod 1 to localhost:8000
   kubectl port-forward -n app-registry-local-dev $POD1 8000:8000
   ```
   
   In a second terminal, forward pod 2 to a different local port:
   ```bash
   # Terminal 2: Get the second pod name
   POD2=$(kubectl get pods -n app-registry-local-dev -l app=app-registry-ui \
     -o jsonpath='{.items[1].metadata.name}')
   echo "Pod 2: $POD2"
   
   # Forward pod 2 to localhost:8001
   kubectl port-forward -n app-registry-local-dev $POD2 8001:8000
   ```
   
   Verify both forwards are active:
   ```bash
   # Terminal 3: Test connectivity
   curl -I http://localhost:8000/
   curl -I http://localhost:8001/
   # Both should return 200 OK
   ```

3. **Prepare test data (if needed):**
   
   If no promotions exist, create one via the gRPC API:
   ```bash
   # Using the forwarded API port (default 50061)
   grpcurl -plaintext -d '{
     "environment_key": "dev",
     "owner_full_name": "test-app",
     "kind": "ARTIFACT_KIND_IMAGE",
     "version": "v1.0.0",
     "idempotency_key": "nfr17a-test-1"
   }' localhost:50061 appregistry.v1.PromotionRegistry/Promote
   ```
   
   Record the `promotion_id` from the response for the next step.

4. **Subscribe to SSE on both pods (Terminal 3):**
   
   Open two concurrent SSE connections, one to each pod's port-forward:
   ```bash
   # Terminal 3A: Subscribe to pod 1
   curl -N -H "Accept: text/event-stream" \
     http://localhost:8000/promotions/<promotion-id>/status/sse
   
   # Terminal 3B (new session): Subscribe to pod 2
   curl -N -H "Accept: text/event-stream" \
     http://localhost:8001/promotions/<promotion-id>/status/sse
   ```
   
   Both connections should receive heartbeats immediately (`:` lines every few seconds).

5. **Publish an event via the broker (Terminal 3, new session):**
   
   Publish a promotion status update to trigger a broadcast:
   ```bash
   grpcurl -plaintext -d '{
     "environment_key": "dev",
     "owner_full_name": "test-app",
     "kind": "ARTIFACT_KIND_IMAGE",
     "version": "v1.0.1",
     "idempotency_key": "nfr17a-publish-1"
   }' localhost:50061 appregistry.v1.PromotionRegistry/Promote
   ```
   
   Or publish directly to RabbitMQ (if gRPC is unavailable):
   ```bash
   kubectl exec -n app-registry-local-dev rabbitmq-dev-0 -- \
     rabbitmqadmin publish exchange=app-registry.promotions.v1 \
     routing_key=promotion.status \
     payload='{"promotion_id":"<id>","status":"published"}'
   ```

6. **Verify both subscribers receive the event:**
   
   In both SSE terminals (3A and 3B), observe that both receive a data message
   within a few seconds. The message should contain the promotion details fragment
   (HTML) or an error indicator. The key is that **both** subscribers receive it.
   
   **Pass criterion:** Both terminals log a non-heartbeat message (lines NOT starting
   with `:`). If only one terminal receives the message, the broker's queue
   fan-out has failed.

7. **Record results (placeholder for manual execution):**
   
   After running the procedure:
   ```
   - Date/time of execution: [PLACEHOLDER — fill in after running]
   - Environment: [docker-desktop, Tilt version X.Y.Z]
   - Pod 1 name: [PLACEHOLDER]
   - Pod 2 name: [PLACEHOLDER]
   - Promotion ID published: [PLACEHOLDER]
   - Pod 1 received event: [PLACEHOLDER — yes/no]
   - Pod 2 received event: [PLACEHOLDER — yes/no]
   - Notes: [PLACEHOLDER — any issues, timing observations, etc.]
   ```

**Cleanup:**

```bash
# Kill port-forwards (Ctrl+C in those terminals)
# Scale back to 1 replica
kubectl scale deployment/app-registry-ui -n app-registry-local-dev --replicas=1
```

---

### NFR17(b) — Real browser path through ingress and Service

**Objective:** Verify that a real browser, reaching the page through a real
ingress and Kubernetes Service (not port-forward), receives pushed SSE updates.
Additionally verify that the `Last-Event-ID` request header survives round-trip
through the ingress intact, which guards against proxy header truncation.

**Prerequisites:**
- Tilt environment running with a real ingress (requires `setup_ingress` or
  similar helper in Tiltfile — **currently requires manual ingress setup**)
- kubectl configured for `docker-desktop` context
- A real browser (Chrome, Firefox, Safari) on your development machine
- UI deployment running (no multi-replica requirement for this test)

**Note on ingress setup:** The current Tiltfile does not include an ingress
resource. To run this test, you must either:
1. Add an ingress resource manually via `kubectl apply`, or
2. Extend the Tiltfile to call `setup_ingress` (if implemented in `common.tilt`)

Example ingress manifest (apply manually if not in Tiltfile):
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: app-registry-ui-ingress
  namespace: app-registry-local-dev
spec:
  ingressClassName: nginx  # or docker-desktop's default
  rules:
  - host: app-registry-ui.localhost
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: app-registry-ui
            port:
              number: 8000
```

**Procedure:**

1. **Ensure the ingress is running:**
   ```bash
   kubectl get ingress -n app-registry-local-dev app-registry-ui-ingress
   # Should show app-registry-ui.localhost with the UI's Service backend
   ```

2. **Create a promotion with known ID (if needed):**
   ```bash
   grpcurl -plaintext -d '{
     "environment_key": "dev",
     "owner_full_name": "test-app-browser",
     "kind": "ARTIFACT_KIND_IMAGE",
     "version": "v1.0.0",
     "idempotency_key": "nfr17b-test-1"
   }' localhost:50061 appregistry.v1.PromotionRegistry/Promote
   ```
   
   Record the promotion ID.

3. **Open a real browser and navigate to the page:**
   
   In Chrome, Firefox, or Safari, open:
   ```
   http://app-registry-ui.localhost/promotions/<promotion-id>
   ```
   
   (Note: localhost hostname resolution for `app-registry-ui.localhost` may
   require adding an entry to `/etc/hosts` on non-Docker-Desktop systems.)

4. **Observe the live indicator and SSE connection:**
   
   The page should:
   - Display the promotion details (name, version, status, etc.)
   - Show a "live" indicator (if FR23 is implemented) in the top-right or
     as a status badge
   - Establish an SSE connection in the browser's Network tab
     (DevTools → Network, filter by `sse` or look for `/promotions/<id>/status/sse`)

5. **Publish an update event:**
   
   In a terminal, promote a new version or manually publish to RabbitMQ:
   ```bash
   grpcurl -plaintext -d '{
     "environment_key": "dev",
     "owner_full_name": "test-app-browser",
     "kind": "ARTIFACT_KIND_IMAGE",
     "version": "v1.0.1",
     "idempotency_key": "nfr17b-publish-1"
   }' localhost:50061 appregistry.v1.PromotionRegistry/Promote
   ```

6. **Verify the browser receives the update:**
   
   Within a few seconds of publishing:
   - The page should update (details change, new version appears, or status updates)
   - The "live" indicator should remain green/active (not flip to "not-live")
   - In DevTools Network tab, the SSE connection should show incoming messages
   - **No page reload or manual refresh should be required.**

7. **Verify Last-Event-ID header round-trip:**
   
   This is an advanced check to confirm the ingress does not truncate headers:
   
   a. Open DevTools → Network, filter by XHR/Fetch to see SSE requests
   b. Click on the SSE connection request and inspect the **Request Headers** section
   c. Look for the `Last-Event-ID` header — it should be present after reconnect
   d. Compare the header value against what the browser's last received event ID was
   e. They should match exactly (same format, no truncation)
   
   Alternatively, check the browser console logs if FR27's error handling logs
   the header value.

8. **Verify connection stability (heartbeat interval):**
   
   Continue observing for at least one heartbeat interval (default ~30s):
   - The SSE connection should remain open
   - Heartbeat lines (`:` comments) should arrive regularly
   - The live indicator should not flip to "not-live" unless you manually stop
     the server
   - No unintended page reloads or connection resets should occur

9. **Record results (placeholder for manual execution):**
   
   After running the procedure:
   ```
   - Date/time of execution: [PLACEHOLDER — fill in after running]
   - Browser: [Chrome/Firefox/Safari version]
   - Environment: [docker-desktop, ingress type]
   - Ingress hostname: [app-registry-ui.localhost or custom]
   - Promotion ID accessed: [PLACEHOLDER]
   - Page loaded successfully: [PLACEHOLDER — yes/no]
   - SSE connection established: [PLACEHOLDER — yes/no]
   - Event received in browser: [PLACEHOLDER — yes/no]
   - Live indicator behavior: [PLACEHOLDER — stayed green/reacted correctly]
   - Last-Event-ID header present: [PLACEHOLDER — yes/no]
   - Last-Event-ID value: [PLACEHOLDER — actual value]
   - Connection stable for ≥1 heartbeat: [PLACEHOLDER — yes/no]
   - Notes: [PLACEHOLDER — proxy errors, header truncation, timing issues, etc.]
   ```

**Cleanup:**

```bash
# Delete the ingress (if manually applied)
kubectl delete ingress -n app-registry-local-dev app-registry-ui-ingress

# Port-forwards terminate on exit
```

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
