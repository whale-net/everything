# app-registry-worker

Temporal worker that drains the `writeback_outbox` and propagates promotion
state to the gitops repo and the S3 snapshot. Built in **AR-4**.

Not yet implemented.

## Why a worker at all

The API must never push to git inside a promotion RPC — a git push is slow,
fails transiently, and cannot participate in the promotion transaction. Instead
the API writes an outbox row atomically with the promotion, and this worker
turns that intent into durable, retryable work.

Temporal cannot enlist in a Postgres transaction, so the outbox — not Temporal —
is what guarantees a promotion is never lost. Temporal is the executor.

## Intended layout

```
worker/
  main.go             # temporal worker bootstrap via libs/go/temporal
  poller/             # outbox drain loop -> StartWorkflow(id = promotion_id)
  workflows/
    writeback.go      # WritebackWorkflow
  activities/
    render.go         # GetEnvironmentState -> rendered values document
    gitops.go         # clone/commit/push, retry on conflict
    snapshot.go       # put env-state.json to S3 (libs/go/s3)
    complete.go       # mark outbox row done, stamp workflow id on the event
```

## Guarantees

- Workflow id is the promotion id, so Temporal dedups redelivered outbox rows.
- Every activity is idempotent and independently retryable.
- `state_hash` from `GetEnvironmentState` short-circuits no-op commits.
- Push conflicts retry by re-reading current state — last writer wins per
  environment file, which is correct since the registry owns that file.

## Dependencies not yet in the repo

`go.temporal.io/sdk` is not in `go.mod`/`MODULE.bazel`, and `libs/go/temporal`
does not exist (`friendly_computing_machine` uses the Python SDK only). Both
land in **AR-1**.

## Release metadata

`app_type: worker`.
