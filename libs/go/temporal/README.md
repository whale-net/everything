# Go Temporal Client/Worker Bootstrap

Shared Temporal SDK plumbing for Go services in the everything monorepo:
env-driven client configuration, a client constructor, a worker bootstrap
helper, and a logging bridge to `libs/go/logging`.

This is the AR-4a foundations library — it makes `go.temporal.io/sdk` build
under Bazel and gives services a consistent way to connect. It does **not**
implement any workflow, activity, or outbox — see
[`tools/app_registry/PLAN-HISTORY.md`](../../../tools/app_registry/PLAN-HISTORY.md)'s
AR-4b section for that.

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

## Tracing

`NewClient` installs [`go.temporal.io/sdk/contrib/opentelemetry`](https://pkg.go.dev/go.temporal.io/sdk/contrib/opentelemetry)'s
tracing interceptor, so workflow and activity execution emit spans against
the global OTel tracer provider. This is a no-op until the process
configures tracing — e.g. via `libs/go/logging`'s
`logging.Configure(logging.Config{EnableTracing: true, ...})`.

## Schedules

`UpsertSchedule(ctx, schedules client.ScheduleClient, opts client.ScheduleOptions) error`
creates the schedule described by `opts`, or -- if a schedule with
`opts.ID` already exists -- patches that schedule's `Spec`/`Action`/
`Overlap` to match `opts` instead of leaving it alone:

```go
schedules := temporalClient.ScheduleClient()
err := temporal.UpsertSchedule(ctx, schedules, client.ScheduleOptions{
    ID:     "my-recurring-thing",
    Spec:   client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: interval}}},
    Action: &client.ScheduleWorkflowAction{Workflow: MyWorkflow, TaskQueue: taskQueue},
    Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
})
```

`client.ScheduleClient.Create`'s own already-exists response
(`temporal.ErrScheduleAlreadyRunning` from `go.temporal.io/sdk/temporal`)
has to be tolerated as success by any idempotent caller building a
create-once, ensure-repeatedly pattern -- but treating it as a pure no-op
means the schedule's parameters (e.g. its interval) are pinned forever to
whatever its first caller passed at creation time and never updated
again, even after the configured value changes and the service restarts.
`UpsertSchedule` closes that gap (see `audience_score_system`'s
`ARCHITECTURE.md` "Temporal: schedule upsert helper" for the issue #1742
incident that motivated it: `ASS_SYNC_INTERVAL` moved from 20m to 24h but
every already-connected Channel kept syncing every 20m).

The existing schedule's `State` (paused/note/limited-actions) is left
untouched by the update -- a caller who paused a schedule by hand won't
have it silently resumed by an unrelated `UpsertSchedule` call.

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
