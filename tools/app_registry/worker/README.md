# app-registry-worker

Temporal worker that drains `writeback_outbox` and runs one
`WritebackWorkflow` per row, rendering promotion state and writing it to a
local path. Built in **AR-4b**. Publishes nowhere else — no gitops commit,
no S3 put; see [`../ARCHITECTURE.md`](../ARCHITECTURE.md) "Writeback: outbox
-> Temporal" and [`../PLAN.md`](../PLAN.md)'s AR-4b section for what is
deliberately out of scope.

## Why a worker at all

The API must never push to git (or write anywhere slow/flaky) inside a
promotion RPC — that work is slow, fails transiently, and cannot
participate in the promotion transaction. Instead the API writes a
`writeback_outbox` row atomically with the promotion (see
`server/handlers/promotion.go`'s `enqueueWriteback`), and this worker turns
that intent into durable, retryable work.

Temporal cannot enlist in a Postgres transaction, so the outbox — not
Temporal — is what guarantees a promotion is never lost. Temporal is the
executor: it retries activities, gives each promotion's writeback a
replayable history, and (via workflow-id collision handling) makes starting
the same promotion's workflow more than once harmless.

## Layout

```
worker/
  main.go               # bootstrap: DB pool, gRPC client to the API, Temporal
                         # client/worker (libs/go/temporal), activity/workflow
                         # registration, outbox drain loop, artifact reaper
                         # loop, graceful shutdown
  outbox/
    drain.go             # Drainer: ClaimBatch -> ExecuteWorkflow -> MarkDone/MarkFailed
    drain_test.go
  reaper/
    reaper.go             # AR-7b (issue #558): periodic sweep of stale
                           # allocated/publishing artifact rows to failed
  writeback/
    workflow.go          # WritebackWorkflow, the Writeback activity interface,
                          # WritebackInput/RenderedState/PublishResult, task queue
    workflow_test.go      # testsuite-based workflow logic tests (no live Temporal)
    stub.go               # StubActivities: the AR-4b stub Writeback implementation
    stub_test.go           # state_hash no-op detection, unit-level
```

## How it works

1. `Promote`/`Rollback` (`server/handlers/promotion.go`) write the SCD2
   promotion row, a `promotion_event` row, and a `writeback_outbox` row, all
   inside one Postgres transaction (see `AGENTS.md` "SCD2" and
   `enqueueWriteback`'s doc comment). If any of the three fails, none of them
   commit — verified against real Postgres in
   `server/repository/postgres/postgres_integration_promotion_test.go`
   (`TestWriteback_EnqueueCommitsAtomicallyWithPromotion` and
   `TestWriteback_EnqueueFailureRollsBackWholeTransaction`).
2. `outbox.Drainer` polls `writeback_outbox` directly against Postgres (a
   direct connection, not routed through the gRPC API — see `Drainer.Store`'s
   doc comment) via `WritebackRepository.ClaimBatch`, an atomic
   `UPDATE ... WHERE outbox_id IN (SELECT ... FOR UPDATE SKIP LOCKED)`
   statement that claims every `'pending'` row plus every `'claimed'` row
   whose claim has gone stale (`WRITEBACK_CLAIM_STALE_AFTER`, see
   [`../ENV.md`](../ENV.md)) — the mechanism that recovers a row after the
   worker holding it is killed mid-run.
3. For each claimed row, `Drainer.startWorkflow` calls
   `client.ExecuteWorkflow` with **workflow id = promotion id** (see
   `writeback.WritebackWorkflow`'s doc comment) and marks the row `'done'`.
   A `serviceerror.WorkflowExecutionAlreadyStarted` (or Temporal
   transparently attaching to an already-open execution of the same id) is
   also treated as success — that is the redelivery case this design exists
   to make harmless. Any other error marks the row back to `'pending'` for
   immediate retry on the next pass.
4. `WritebackWorkflow` (`writeback/workflow.go`) calls exactly two
   activities, both dispatched by name (not Go function value — see the
   `Writeback` interface's doc comment for why): `RenderEnvironmentState`,
   then `Publish`. All I/O lives in the activity implementation; the
   workflow function itself does nothing but sequence two
   `workflow.ExecuteActivity` calls, per the "Workflow determinism" hazard
   in `AGENTS.md`/`PLAN.md`.
5. `StubActivities` (`writeback/stub.go`) implements `Writeback` for AR-4b:
   `RenderEnvironmentState` reads state via the App Registry API's
   `GetEnvironmentState` RPC (never Postgres directly — see
   `ARCHITECTURE.md` "The API is the write path; git is the delivery path")
   and marshals it to JSON; `Publish` writes that JSON to
   `WRITEBACK_OUTPUT_DIR/<environment_key>.json`, skipping the write
   (`Skipped: true`) when a sidecar `.state_hash` file already matches —
   the state_hash no-op detection the real implementation inherits without
   any interface change.

## The `Writeback` activity interface

```go
type Writeback interface {
    RenderEnvironmentState(ctx context.Context, in WritebackInput) (RenderedState, error)
    Publish(ctx context.Context, state RenderedState) (PublishResult, error)
}
```

This is the whole contract AR-4 exists to deliver (see `ARCHITECTURE.md`
"Resolved questions" #2). A real gitops-committer implementation — one whose
`Publish` clones/commits/pushes a git repo and puts an S3 snapshot — plugs
in behind this exact interface: `WritebackWorkflow`, `WritebackInput`,
`RenderedState`, and `PublishResult` all stay as they are, and `main.go`
only needs to construct a different `Writeback` and register it under the
same two activity names. No schema, proto, or workflow change.

## `state_hash` no-op detection

`server/handlers/promotion.go`'s `stateHash` function computes a content
hash over an environment's current promoted state (`promotion_id`,
artifact `digest`, `state`, `is_override`, sorted by `promotion_id` for
determinism) and is called from two places with results that are guaranteed
to agree immediately after a promotion's transaction commits:

- `GetEnvironmentState`'s response (`state_hash` field).
- `enqueueWriteback`, storing the value on the new `writeback_outbox` row —
  computed inside the *same* transaction as the promotion write.

`StubActivities.RenderEnvironmentState` re-reads `GetEnvironmentState` (not
the outbox row's stored hash) so it always renders the freshest state, and
copies that response's `state_hash` onto the `RenderedState` it returns.
`StubActivities.Publish` then compares that hash against the sidecar file
from its last successful write for the same `environment_key` and skips
writing when they match. This is what makes a redundant `WritebackWorkflow`
execution (e.g. a reclaim-and-retry after a killed worker) cheap rather than
merely harmless — see `stub_test.go`'s
`TestStubActivities_Publish_SkipsNoOpWrite`, verified to fail when the check
is deliberately removed (see the phase report for the transcript).

## Stale-row reaper (AR-7b, issue #558)

A third loop, alongside the outbox drain loop, in this same process:
`reaper.Reaper` calls `ArtifactRepository.ExpireStale` on a timer, moving
every `artifact` row still `allocated` or `publishing` after
`ARTIFACT_REAPER_TIMEOUT` (measured from `state_changed_at`) to `failed`
with `fail_reason = 'stale'`. See `../ARCHITECTURE.md` "Artifact lifecycle:
allocated -> publishing -> published" and "The reaper is not optional".

This exists because AR-7b's release plan step writes an `allocated`
artifact row *before anything is pushed*, at every adoption stage — the
`UNIQUE (owner_id, kind, version)` index that makes concurrent
`AllocateVersion` calls collision-safe is the same index that would
otherwise let a cancelled or crashed release run hold a version number
forever, permanently blocking that owner's next release. The reaper is what
turns that hold into a bounded one.

Same shape as `outbox.Drainer`: connects to Postgres directly (not through
the gRPC API), sweeps once immediately on startup so a worker restart
doesn't leave an already-stale row idle for a full poll interval, then
again on every `ARTIFACT_REAPER_POLL_INTERVAL` tick until the process is
asked to shut down. A sweep error is logged and retried on the next tick,
never fatal — the worker's dependencies being briefly unreachable must not
crash-loop the process.

## Configuration

See [`../ENV.md`](../ENV.md) "Worker (`app-registry-worker`, AR-4b)" and
"Artifact reaper (AR-7b, issue #558)" for every environment variable.

## Testing

Unit tests (no live Postgres or Temporal required):

```bash
bazel test //tools/app_registry/worker/...
```

- `writeback/workflow_test.go` uses the Temporal SDK's `testsuite` to drive
  `WritebackWorkflow` against mocked activities — proves the
  render-then-publish sequencing and that a `RenderEnvironmentState` failure
  short-circuits before `Publish` runs.
- `writeback/stub_test.go` exercises `StubActivities.Publish`'s no-op
  detection directly against a temp directory.
- `outbox/drain_test.go` exercises `Drainer.startWorkflow`'s three outcomes
  (success, already-started, other-error) against a mocked
  `go.temporal.io/sdk/mocks.Client` and a fake `WritebackRepository`.

Real-Postgres coverage for the outbox's atomicity and claim-locking lives in
`server/repository/postgres/postgres_integration_promotion_test.go` (see
[`../TESTING.md`](../TESTING.md#running-the-postgres-integration-tier-ar-2d)).

For exercising the worker as a running process — locally via Tilt, or a
from-scratch manual run against Docker containers plus `bazel run` — and how
the "killed mid-run" exit criterion was verified, see
[`../TESTING.md`](../TESTING.md#writeback-worker-ar-4b).

## Release metadata

`app_type: worker` (long-running, no port — distinct from `job`, which
`migrate`/`cli` use for run-to-completion binaries). Domain `app-registry` +
name `worker` produces the `app-registry-worker` image/app identity,
included in the App Registry Helm chart
(`//tools/app_registry:app_registry_chart`). Cross-compiles to `arm64`
cleanly — same pure-Go dependency shape as `api`/`migration`; see
[`../../../docs/DOCKER.md`](../../../docs/DOCKER.md).
