# Shitposter brain job runner

The brain job runner is the shared shape for Shitposter persona jobs (harvest,
reflect, write, snapshot, idea_generate). Each job kind plugs a compute step and an apply step
into it. The harvest (`harvest.py`), snapshot (`snapshot.py`), and reflect (`reflect.py`) job bodies are
registered, and so are write and idea-generate (`idea_generate.py`). Worker startup registers the harvest, reflect, write, snapshot and idea_generate
schedules for every persona (see [Schedules](#schedules)); a successful reflect also
enqueues a snapshot.

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

`register_harvest_schedule(...)` creates the hourly schedule at worker startup. `brain-trigger <persona_id> harvest` runs it on demand.

## Reflect job

`reflect.py` applies one daily round of validated changes to the persona's active
attributes. Compute gathers unconsumed engagement rows and promoted suggestions,
newest first, capped at `FCM_SHITPOSTER_REFLECTOR_INPUT_CAP`; the excess carries to
the next run. With no input the run records `no_op` and the agent is not called.
Otherwise the reflector agent (definition `shitposter-reflector`, overridable with
`FCM_SHITPOSTER_REFLECTOR_AGENT_ID`) returns
`{"ops": [...]}` with `add`, `reinforce`, `retire`, and `merge` ops, each citing
refs such as `post:<id>` or `suggestion:<id>`. The agent's system prompt (`shitposter-reflector` in
`whagent_net/config/agents.yaml`) restates this op schema; change both together.

Each op is rejected, changing nothing, with one of these reasons:

- `malformed`: wrong shape, unknown op, empty or over-long text
- `no_valid_cause`: no cited ref is an input of this run
- `missing_attribute`: names a key that is not an active attribute
- `targets_member`: add text identifies a current or former community member (see [What identifies a member](#what-identifies-a-member))
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

`register_reflect_schedule(...)` creates the daily schedule at worker startup. `brain-trigger <persona_id> reflect` runs it on demand. The
snapshot job after a successful run: the workflow starts the persona's snapshot run as a
child once the apply commits. No-op and failed runs enqueue nothing.

## Snapshot job

`snapshot.py` renders a persona's memory (attributes, then the top N ranked lore,
then one optional random pick from lore ranked below N) into a context snapshot
within `FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET`. N is `FCM_SHITPOSTER_SNAPSHOT_RANKED_LORE_CAP`.
Suggestion text is never read; only derived attributes are. Tunables are in
[ENV.md](../ENV.md).

Theme and novel lore (written by the idea-generator) are ranked with the same
score as hit lore, but a snapshot never holds more generated lore entries than
non-generated ones; with no non-generated lore included, it holds at most one.
A generated candidate that would break this is skipped, including the random
pick. Attributes do not count on either side.

## Write job

`write.py` drafts a batch of posts from the latest context snapshot into the
`shitposterdraft` queue. Compute reads that snapshot and sends it to the writer
agent (definition `shitposter-drafter`, overridable with `FCM_SHITPOSTER_WRITER_AGENT_ID`) as the first turn, or as pinned context
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
`FCM_SHITPOSTER_WRITE_CADENCE_HOURS` hours. Worker startup registers it for every persona;
`brain-trigger <persona_id> write` runs it on demand.

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
Otherwise the reflector agent (definition `shitposter-reflector`, overridable with
`FCM_SHITPOSTER_REFLECTOR_AGENT_ID`) returns
`{"ops": [...]}` with `add`, `reinforce`, `retire`, and `merge` ops, each citing
refs such as `post:<id>` or `suggestion:<id>`.

Each op is rejected, changing nothing, with one of these reasons:

- `malformed`: wrong shape, unknown op, empty or over-long text
- `no_valid_cause`: no cited ref is an input of this run
- `missing_attribute`: names a key that is not an active attribute
- `targets_member`: add text identifies a current or former community member (see [What identifies a member](#what-identifies-a-member))
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

`register_reflect_schedule(...)` creates the daily schedule at worker startup. `brain-trigger <persona_id> reflect` runs it on demand. The
snapshot trigger after a successful run is wired (see the reflect section above).

## Idea-generator job

`idea_generate.py` keeps generated lore near its standing-pool target. A run has
three steps, all applied in the runner's single transaction: emit theme/novel
entries, refresh generated lore's earned popularity (`lore_refresh.py`), and step
the theme/novel target weights (`source_weights.py`). Hit promotion is not done
here; `lore_promotion.py` owns it.

**Targets.** `current_weights(...)` reads the persona's current rows in
`fcm.shitpostersourceweight` (SCD2, one current row per source, each revision
carrying a JSON `cause`). The first read seeds them from
`FCM_SHITPOSTER_THEME_TARGET_WEIGHT` and `FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT`
(cause `initial`, or `fallback` when those were outside their bounds or over the
combined ceiling; see `ENV.md`). Compute then runs the weight step (below)
first, so a weight it steps is the target this run emits against.
`/shitpost admin lore` shows current weights, bounds and ceiling.

**Shares.** Each generated kind's share is its count over all active lore. With
no active lore, both shares are `null` and count as below target.

**Novel gate.** Read before any agent call, so a failed run still records it.
It compares the mean popularity of posts whose engagement finalized in the last
`FCM_SHITPOSTER_NOVEL_GATE_RECENT_DAYS` against the last
`FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS`. It is open (`engagement_drop`) only
when recent < `FCM_SHITPOSTER_NOVEL_GATE_FRACTION` x baseline; otherwise it is
closed with `no_engagement_drop`, or `no_engagement_in_window` when either window
has no finalized engagement.

Branches run in order, theme first; each is asked only for the run-cap slots the
earlier branch left:

- **Theme.** When the theme share is below target, the theme agent
  (`shitposter-theme-generator`, overridable with
  `FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID`) gets the active lore and the
  operator-retired attributes and lore. It returns
  `{"entries": [{"text", "cites": [lore_id, ...]}]}`.
- **Novel.** Only while the novel gate is open and the novel share is below
  target, the novel agent (`shitposter-novel-generator`, overridable with
  `FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID`) gets the persona's active
  attributes (`id`, `text`), the active lore, and the operator-retired
  attributes and lore. Engagement figures are never sent. It returns
  `{"entries": [{"text"}]}`.

A reply without a JSON `entries` list, or an agent error or deadline, fails the run.

Each proposal is rejected, writing nothing and never failing the run, with one of these reasons:

- `malformed`: missing, empty or over-long text
- `insufficient_citations`: a theme entry citing fewer than two active lore ids
- `targets_member`, `instruction`: the reflector's member-name and instruction checks
- `retired_readd`: the text matches or rewords (high word overlap) operator-retired lore or an attribute
- `run_cap`: past `FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP` (default 2) entries across theme and novel

The guardrail checks live in `shitposter_brain/guardrails.py`, shared with the reflector.

### What identifies a member

`guardrails.member_identifiers` builds the set both jobs check (whole-word,
case-insensitive), from every non-bot `slackuser` row, including users who have
left the workspace:

- the stored `name`, `display_name` and `real_name` (Slack user sync stores the
  last two separately)
- each word of those names at least 3 characters long that is not a common word
  (so a first or last name alone matches, but "will" from "Will Smith" does not)
- every alias in `FCM_SHITPOSTER_MEMBER_ALIASES` (operator-maintained nicknames)

For idea-generator proposals, a Slack mention (`<@U...>`, `<!here>` and similar) is also `targets_member`.

**Entries.** Apply writes each accepted entry as `shitposterloreentry`
(`kind=theme` or `kind=novel`), with `popularity_score` set to the median
popularity of active hit lore, or 0 when there is none, and a
`shitpostermemorychange` add row:

- theme: `cause_kind=theme_synthesis`, `cause_ref` listing the cited entries
  (`lore:<id>,lore:<id>`). A cited entry retired between compute and apply is
  dropped; a theme entry left with fewer than two is rejected `insufficient_citations`.
- novel: `cause_kind=novel_gate`, `cause_ref` the gate reading as JSON
  (`{"baseline_mean", "fraction", "recent_mean"}`, keys sorted).

**Refresh step** (`lore_refresh.py`, after the entries). Each active theme/novel
entry's popularity is recomputed as the mean popularity of finalized posts
written from snapshots that held any revision of the entry (matched by
`lore_key`). An entry with no such posts, or already at that mean, is left
alone; otherwise apply opens a new SCD2 revision under the same `lore_key`. A
planned refresh whose row was retired or revised since compute is skipped.

**Weight step** (`source_weights.py`). Per source, compute compares the mean
popularity of finalized posts whose snapshot held that source's lore ("with")
against posts without it, over days in the last `FCM_SHITPOSTER_WEIGHT_WINDOW_DAYS`
on which both kinds were written. With higher "with" mean the weight steps up by
`FCM_SHITPOSTER_WEIGHT_STEP`, lower steps it down, clamped to the source's
bounds. It stays put with one of these reasons:

- `below_minimum`: either group has fewer than `FCM_SHITPOSTER_WEIGHT_MIN_POSTS` posts
- `equal_means`: the two means are equal
- `bound`: the step is clamped to no change
- `ceiling`: the step would push theme + novel above
  `FCM_SHITPOSTER_SOURCE_WEIGHT_CEILING`; when both rise and only one fits, the
  one further below its own upper bound steps
- `superseded`: the stored weight moved between compute and apply

Apply writes a `shitpostersourceweight` revision (cause
`{with_mean, without_mean, with_n, without_n, days_compared}`, `run_id` set) for
each stepped source. A failed run writes no weight revisions.

**Status.** `failed` when compute fails, e.g. an agent error (details keep the share
and gate readings and any rejections; nothing is written). Otherwise
`succeeded` when any entry is written, any refresh is applied, or any weight
steps; else `no_op`. Run details:

```
{"theme": {"share", "target"},
 "novel": {"share", "target",
           "gate": {"open", "recent_mean", "baseline_mean", "fraction", "reason"}},
 "entries": [{"lore_id", "kind", "agent_id", "cited"}],
 "rejections": [{"kind", "text", "reason"}],
 "refreshes": [{"lore_id", "lore_key", "old", "new", "post_count"}],
 "weight_changes": [{"source", "old", "new", "with_mean", "without_mean"}
                    | {"source", "unchanged_reason"}]}
```

`share` is `null` when the pool is empty; gate means are `null` when their
window has no finalized engagement. A refresh row's `lore_id` is the new revision.

`/shitpost admin ideas` labels a `no_op` run "every proposal rejected (n)" when
there are rejections, "shares at or above target" when both shares are, and
otherwise per branch: `theme at/above target` or `theme: agent proposed
nothing`, and `novel gate closed (<reason>)`, `novel at/above target` or
`novel: agent proposed nothing`.

Runs every `FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS` (default 24) and on demand with
`brain-trigger <persona_id> idea_generate`. Setting the cadence to `off` removes the
persona's schedule at the next worker start; `brain-trigger` still works. Runs share the
per-persona lock, so one overlapping another brain job is recorded `skipped`, and a
failed or timed-out run writes no lore, refreshes or weight revisions.

## Timeouts

Per-kind workflow timeouts live in `BRAIN_JOB_TIMEOUTS` in `temporal/shitposter_brain/base.py`:

| Kind | Timeout |
|---|---|
| harvest | 15 min |
| reflect | 30 min |
| write | 30 min |
| snapshot | 5 min |
| idea_generate | 30 min |

`STALE_LOCK_SLACK` is 5 min. Both are code constants, not environment variables.
See [ENV.md](../ENV.md#shitposter-brain-job-runner).

## Operator trigger: `brain-trigger`

Runs one job now through the same per-persona lock as scheduled runs. It is a
command on the `workflow` CLI (`src/friendly_computing_machine/cli/workflow_cli.py`):

```
fcm workflow brain-trigger <persona_id> <harvest|reflect|write|snapshot|idea_generate>
```

It prints `run_id=<n> status=<status>`, plus `skip_reason=...` when the run was
skipped because another run held the lock.

Needs `TEMPORAL_HOST` and the usual worker environment (see [ENV.md](../ENV.md)).

## Schedules

`register_brain_schedule(...)` in `control.py` creates a Temporal Schedule per
persona and job kind, id `fcm-<app_env>-shitposter-brain-<kind>-<persona_id>`,
with overlap policy `SKIP`. It is a no-op when the schedule already exists.

`temporal/shitposter_brain/schedules.py` `register_brain_schedules_async` runs at worker
startup (from `run_worker`) and calls the harvest, reflect, write, snapshot and idea_generate registrars for
every row in `shitposter_dal.list_persona_ids()`. Each registration is logged and skipped on
failure, so one bad persona or a Temporal error does not stop the worker. Restarts create
no duplicates. Write cadence is `FCM_SHITPOSTER_WRITE_CADENCE_HOURS`; snapshot cadence is
`FCM_SHITPOSTER_SNAPSHOT_CADENCE_HOURS` (default 24); idea_generate cadence is
`FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS` (default 24). When that is `off`,
`remove_brain_schedule(...)` deletes the persona's idea_generate schedule (a no-op if none
exists) and none is created. An existing schedule keeps its interval until removed, so
changing a cadence needs the schedule deleted first. A successful reflect still enqueues a
snapshot on top of the schedule.

## Verifying

- Migration: `bazel test //friendly_computing_machine/tests:test_shitposter_migration`.
- Runner behaviour (overlap skip, all-or-nothing apply, timeout to failed):
  `bazel test //friendly_computing_machine/tests:test_shitposter_brain_job_runner`.
- Manual, against a local Temporal (Tilt): run `brain-trigger` twice for the same
  persona and kind while one is in flight. The second prints `status=skipped` and
  a `skip_reason`, and the Temporal UI shows one completed workflow.
