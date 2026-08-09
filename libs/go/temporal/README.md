# Go Temporal Client/Worker Bootstrap

Shared Temporal SDK plumbing for Go services in the everything monorepo:
env-driven client configuration, a client constructor, a worker bootstrap
helper, and a logging bridge to `libs/go/logging`.

This is the AR-4a foundations library — it makes `go.temporal.io/sdk` build
under Bazel and gives services a consistent way to connect. It does **not**
implement any workflow, activity, or outbox — see
[`tools/app_registry/PLAN.md`](../../../tools/app_registry/PLAN.md) AR-4b for
that.

## Usage

```go
import (
    "go.temporal.io/sdk/worker"

    "github.com/whale-net/everything/libs/go/temporal"
)

cfg := temporal.ConfigFromEnv()

c, err := temporal.NewClient(cfg, temporal.NewLogger("app-registry"))
if err != nil {
    log.Fatal(err)
}
defer c.Close()

w := temporal.NewWorker(c, cfg.TaskQueue, worker.Options{})
w.RegisterWorkflow(MyWorkflow)
w.RegisterActivity(MyActivity)
if err := w.Run(worker.InterruptCh()); err != nil {
    log.Fatal(err)
}
```

`NewClient`'s `client.Dial` connects eagerly — a non-nil error means the
Temporal frontend was unreachable (or misconfigured), not just that options
were invalid.

## Environment Variables

| Variable | Default | Description |
|----------|---------|--------------|
| `TEMPORAL_HOST` | `localhost:7233` | Temporal frontend service `host:port`. Named to match `friendly_computing_machine`'s existing `TEMPORAL_HOST` convention (its Python-SDK usage is configured independently — this variable name is shared for consistency, not code). |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | *(none)* | Default task queue name. `ConfigFromEnv` does not fall back to anything — callers that need a task queue must set this or pass one explicitly. |

## Logging Bridge

Temporal's SDK takes a `go.temporal.io/sdk/log.Logger` interface, not `slog`
directly. `NewLogger(name string)` wraps `logging.Get(name)` (this repo's
`libs/go/logging`) with the SDK's built-in `log.NewStructuredLogger`, so
client and worker logs go through the same structured pipeline
(console/JSON output, OTLP export) as the rest of the service — no second
logging stack.

```go
logger := temporal.NewLogger("temporal-worker")
c, _ := temporal.NewClient(cfg, logger)
```

The worker SDK has no separate per-worker `Logger` option in
`worker.Options`; workers use the logger set on the `client.Client` they are
built from.

## Testing

Config parsing and the logging bridge are unit tested without a live
Temporal server (`config_test.go`, `logger_test.go`). `NewClient` is not
unit tested — `client.Dial` connects eagerly, so exercising it needs a real
server; see [`tools/tilt/common.tilt`](../../../tools/tilt/common.tilt)'s
`setup_temporal` and
[`tools/app_registry/TESTING.md`](../../../tools/app_registry/TESTING.md)
for running one locally via Tilt.

```bash
bazel test //libs/go/temporal/...
```

## Bazel Dependency

```starlark
deps = ["//libs/go/temporal"]
```
