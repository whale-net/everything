# Shitposter brain job runner

The brain job runner is the shared shape for Shitposter persona jobs (harvest,
reflect, write, snapshot). Each job kind plugs a compute step and an apply step
into it. The harvest (`harvest.py`), snapshot (`snapshot.py`), and reflect (`reflect.py`) job bodies are
registered, and so is write. Nothing schedules jobs by default yet; the runner, the per-kind schedule
helper, and the operator trigger are the parts that exist.

Code: `src/friendly_computing_machine/temporal/shitposter_brain/`
(`base.py` constants and job-body registry, `activity.py` lock/compute/apply/fail,
`workflow.py` `ShitposterBrainJobWorkflow`, `control.py` trigger and schedule
helpers), `harvest.py`, `snapshot.py`, `reflect.py`, and `write.py` the job bodies. Registered in `temporal/worker.py`.

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

## Reflect job

`reflect.py` applies one daily round of validated changes to the persona's active
attributes. Compute gathers unconsumed engagement rows and promoted suggestions,
newest first, capped at `FCM_SHITPOSTER_REFLECTOR_INPUT_CAP`; the excess carries to
the next run. With no input the run records `no_op` and the agent is not called.
Otherwise the reflector agent (`FCM_SHITPOSTER_REFLECTOR_AGENT_ID`) returns
`{"ops": [...]}` with `add`, `reinforce`, `retire`, and `merge` ops, each citing
refs such as `post:<id>` or `suggestion:<id>`. The agent's system prompt (`shitposter-reflector` in
`whagent_net/config/agents.yaml`) restates this op schema; change both together.

Each op is rejected, changing nothing, with one of these reasons:

- `malformed`: wrong shape, unknown op, empty or over-long text
- `no_valid_cause`: no cited ref is an input of this run
- `missing_attribute`: names a key that is not an active attribute
- `targets_member`: add text names a current community member (M6 guardrail check)
- `instruction`: `kind` is `instruction`, or the text reads as a directive
- `negative_reinforce`: reinforce cites an engagement whose negative reactions outnumber the rest
- `retired_readd`: add matches an operator-retired item and all cited evidence predates the retirement
- `key_exists`, `conflicting_op`: the key is already active, or an earlier op in the run touched it
- `attribute_cap`: an addition that would take active attributes over `FCM_SHITPOSTER_ATTRIBUTE_CAP`

The cap drops trailing additions first; retires and merges always apply.

Apply writes the accepted ops through the memory DAL with the cited refs as cause,
the `shitposterreflectorrun` row, the consumed-input markers, and the applied or
declined outcome for each consumed suggestion, all in the runner's transaction.
`retire` and `merge` ops call the fold hook (`register_fold_hook`) inside that
transaction if one is registered.

`register_reflect_schedule(...)` creates the daily schedule. Nothing calls it at
worker startup yet. `brain-trigger <persona_id> reflect` runs it on demand. The
snapshot job after a successful run: the workflow starts the persona's snapshot run as a
child once the apply commits. No-op and failed runs enqueue nothing.

## Snapshot job

`snapshot.py` renders a persona's memory (attributes, then the top N ranked lore,
then one optional random pick from lore ranked below N) into a context snapshot
within `FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET`. N is `FCM_SHITPOSTER_SNAPSHOT_RANKED_LORE_CAP`.
Suggestion text is never read; only derived attributes are. Tunables are in
[ENV.md](../ENV.md).

## Write job

`write.py` drafts a batch of posts from the latest context snapshot into the
`shitposterdraft` queue. Compute reads that snapshot and sends it to the writer
agent (`FCM_SHITPOSTER_WRITER_AGENT_ID`) as the first turn, or as pinned context
when the client supports it. The reply must be a JSON array of `{text, rank}`.
Each item is checked: `text` is non-empty and at most 280 characters, `rank` is a
positive integer that is not a boolean, and no rank repeats. Items that fail are
dropped and counted. Valid drafts beyond `FCM_SHITPOSTER_DRAFT_BATCH_SIZE` are
dropped and counted too.

The apply step stores the drafts with the snapshot id and run id, and records
`drafted`, `dropped`, `snapshot_id`, and `whagent_session_id` in the run's
`details`. A run with no valid draft, or an unparseable reply, ends `failed` with
the reason in `error`, and nothing is queued. Guardrails are not checked here;
they run at posting time.

`register_write_schedule(...)` creates the schedule, every
`FCM_SHITPOSTER_WRITE_CADENCE_HOURS` hours. Like the other kinds, nothing calls it
at worker startup yet; `brain-trigger <persona_id> write` runs it on demand.

Scheduled posts consume the queue in `temporal/shitposter/activity.py`
(`post_queued_draft_activity`), called by `ShitpostWorkflow` before generation.
It takes the best-ranked draft that is unused, unexpired (`FCM_SHITPOSTER_DRAFT_EXPIRY_HOURS`),
and has no retired item in its snapshot. It discards any draft whose snapshot
holds an attribute or lore entry the operator has since retired
(`retired_item`) or that fails the guardrails (`guardrail:<reason>`), and tries
the next. The post and the draft's `used_at`/`used_post_id` are written in one
transaction, and the post carries the draft's snapshot id. With no usable draft,
the scheduled post is generated on the spot as before.

## Reflect job

`reflect.py` applies one daily round of validated changes to the persona's active
attributes. Compute gathers unconsumed engagement rows and promoted suggestions,
newest first, capped at `FCM_SHITPOSTER_REFLECTOR_INPUT_CAP`; the excess carries to
the next run. With no input the run records `no_op` and the agent is not called.
Otherwise the reflector agent (`FCM_SHITPOSTER_REFLECTOR_AGENT_ID`) returns
`{"ops": [...]}` with `add`, `reinforce`, `retire`, and `merge` ops, each citing
refs such as `post:<id>` or `suggestion:<id>`.

Each op is rejected, changing nothing, with one of these reasons:

- `malformed`: wrong shape, unknown op, empty or over-long text
- `no_valid_cause`: no cited ref is an input of this run
- `missing_attribute`: names a key that is not an active attribute
- `targets_member`: add text names a current community member (M6 guardrail check)
- `instruction`: `kind` is `instruction`, or the text reads as a directive
- `negative_reinforce`: reinforce cites an engagement whose negative reactions outnumber the rest
- `retired_readd`: add matches an operator-retired item and all cited evidence predates the retirement
- `key_exists`, `conflicting_op`: the key is already active, or an earlier op in the run touched it
- `attribute_cap`: an addition that would take active attributes over `FCM_SHITPOSTER_ATTRIBUTE_CAP`

The cap drops trailing additions first; retires and merges always apply.

Apply writes the accepted ops through the memory DAL with the cited refs as cause,
the `shitposterreflectorrun` row, the consumed-input markers, and the applied or
declined outcome for each consumed suggestion, all in the runner's transaction.
`retire` and `merge` ops call the fold hook (`register_fold_hook`) inside that
transaction if one is registered.

`register_reflect_schedule(...)` creates the daily schedule. Nothing calls it at
worker startup yet. `brain-trigger <persona_id> reflect` runs it on demand. The
snapshot trigger after a successful run is not wired.

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
