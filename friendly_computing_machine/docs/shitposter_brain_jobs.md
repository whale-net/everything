# Shitposter brain job runner

The brain job runner is the shared shape for Shitposter persona jobs (harvest,
reflect, write, snapshot). Each job kind plugs a compute step and an apply step
into it. Only the harvest job is registered so far (`harvest.py`). Nothing schedules jobs
by default yet; the runner, the per-kind schedule helper, and the operator trigger
are the parts that exist.

Code: `src/friendly_computing_machine/temporal/shitposter_brain/`
(`base.py` constants and job-body registry, `activity.py` lock/compute/apply/fail,
`workflow.py` `ShitposterBrainJobWorkflow`, `control.py` trigger and schedule
helpers), `harvest.py` the harvest job body. Registered in `temporal/worker.py`.

## Guarantees

- **One job per persona at a time.** Taking the lock inserts a `running` row for
  the persona. Partial unique index `uq_shitposterbrainjobrun_running` enforces
  it. A job that finds a `running` row records a `skipped` row with a skip reason
  and returns. Skips are recorded, never queued.
- **Stale locks are taken over.** A `running` row older than the job timeout plus
  `STALE_LOCK_SLACK` has no live workflow behind it, so the next run takes it over.
- **All-or-nothing apply.** The compute step writes nothing. The final apply
  activity writes the job's changes, the run's `succeeded`/`no_op` status, and the
  input-consumption markers in one transaction. An exception rolls all of it back.
- **Failure leaves inputs for next time.** A body failure or timeout marks the run
  `failed` with `error` set and fails the workflow, so it shows failed in the
  Temporal UI. Nothing is marked consumed, so the next run picks the inputs up.
- **Service subject.** Brain jobs run as FCM's service subject with no
  `on_behalf_of`.

Run statuses: `running`, `succeeded`, `failed`, `skipped`, `no_op`. Trigger values:
`schedule`, `operator`. The table is `shitposterbrainjobrun` (append-only; see
`models/shitposter.py`).

## Harvest job

`harvest.py` finalizes each bot post's engagement once the post is 24h old. For
each persona-owned post in a currently opted-in channel with no engagement row:

- Reactors are distinct human users with an active reaction at finalization (added
  by then, not removed before it). Bot users and reactions flagged `is_bot` are
  excluded. Per-emoji counts are distinct users.
- `negative_reactions` sums the per-emoji counts for emoji on
  `FCM_SHITPOSTER_NEGATIVE_EMOJI`.
- `distinct_repliers` counts distinct non-bot users replying in the post's thread.

Rows go into `shitposterpostengagement` in the apply transaction. `post_id` is the
primary key, so a finalized record is never rewritten. A rerun with nothing new
records `no_op`.

`register_harvest_schedule(...)` creates the hourly schedule. Nothing calls it at
worker startup yet; `brain-trigger <persona_id> harvest` runs it on demand.

## Timeouts

Per-kind workflow timeouts live in `BRAIN_JOB_TIMEOUTS` in `temporal/shitposter_brain/base.py`:

| Kind | Timeout |
|---|---|
| harvest | 15 min |
| reflect | 30 min |
| write | 30 min |
| snapshot | 5 min |

`STALE_LOCK_SLACK` is 5 min. Both are code constants, not environment variables.
See [ENV.md](../ENV.md#shitposter-brain-job-runner).

## Operator trigger: `brain-trigger`

Runs one job now through the same per-persona lock as scheduled runs. It is a
command on the `workflow` CLI (`src/friendly_computing_machine/cli/workflow_cli.py`):

```
fcm workflow brain-trigger <persona_id> <harvest|reflect|write|snapshot>
```

It prints `run_id=<n> status=<status>`, plus `skip_reason=...` when the run was
skipped because another run held the lock.

Needs `TEMPORAL_HOST` and the usual worker environment (see [ENV.md](../ENV.md)).

## Schedules

`register_brain_schedule(...)` in `control.py` creates a Temporal Schedule per
persona and job kind, id `fcm-<app_env>-shitposter-brain-<kind>-<persona_id>`,
with overlap policy `SKIP`. It is a no-op when the schedule already exists. Nothing
calls it yet; the harvester/reflector/writer/snapshot tasks do.

## Verifying

- Migration: `bazel test //friendly_computing_machine/tests:test_shitposter_migration`.
- Runner behaviour (overlap skip, all-or-nothing apply, timeout to failed):
  `bazel test //friendly_computing_machine/tests:test_shitposter_brain_job_runner`.
- Manual, against a local Temporal (Tilt): run `brain-trigger` twice for the same
  persona and kind while one is in flight. The second prints `status=skipped` and
  a `skip_reason`, and the Temporal UI shows one completed workflow.
