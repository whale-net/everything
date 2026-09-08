# Temporal: schedule upsert helper

`//libs/go/temporal` (the shared Go bootstrap `worker` uses for its
client/worker construction) has no equivalent to
`friendly_computing_machine`'s hand-rolled `AbstractScheduleWorkflow`
(`temporal/base.py`) — the only in-repo *scheduled*-workflow precedent, and
it's Python/FCM-specific, not a shared library. `worker` inherits this gap
from `product/01-current-state.md` and knowingly accepts it for M1: issue
#1574 built the per-Channel sync schedule (NFR4, ~1-24 hour interval,
default 24h) directly against the Temporal Go SDK's native
`ScheduleClient` (`audience_score_system/worker/sync.ScheduleManager`,
wrapping `client.Client.ScheduleClient()`), not a repo-shared helper.
`ScheduleManager` is a small, four-method interface
(`EnsureSchedule`/`RemoveSchedule`/`Reconcile`/`TriggerNow`, the last
added by issue #1650 to back the `trigger_channel_sync` MCP tool) with
nothing app-registry-specific about its shape; it still stays local to
`worker/sync`, since a one-off abstraction extracted from a single caller
tends to guess wrong about what a second caller will actually need.

What *did* get promoted, by issue #1742, is the one piece of that
duplication with a real, demonstrated bug behind it:
`client.ScheduleClient.Create`'s already-exists response
(`sdktemporal.ErrScheduleAlreadyRunning`) has to be tolerated as success
by any idempotent caller, but `EnsureSchedule` originally treated it as a
pure no-op -- so a schedule's `Spec`/`Action`/`Overlap` were pinned
forever to whatever its first creator passed, and changing
`ASS_SYNC_INTERVAL` (e.g. 20m → 24h) never took effect for an
already-connected Channel no matter how many times `worker` restarted.
`temporallib.UpsertSchedule(ctx, schedules, opts)`
(`libs/go/temporal/schedule.go`) generalizes the fix: `Create`, and on
already-exists, `GetHandle(opts.ID).Update` to reconcile the existing
schedule's `Spec`/`Action`/`Overlap` to `opts` (its `State` -- paused/note
-- is left untouched, so a hand-paused schedule doesn't get silently
resumed). `worker/sync.ScheduleManager.EnsureSchedule` now calls this
instead of `Schedules.Create` directly -- see "Interval-consistency
caveat" above for what that changes for `ASS_SYNC_INTERVAL` specifically.
The helper is generic Temporal SDK plumbing (same bar `NewClient`/
`NewWorker` already clear), not `ScheduleManager`'s app-specific shape, so
it lives in `//libs/go/temporal` even with only one caller today.

