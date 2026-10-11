# Shitposter brain: operator runbook

Operator guide for the Shitposter persona's memory: reading what changed and
why, reading reflector runs, retiring entries, and tracing a post to the
context snapshot it was written from.

Code: `src/friendly_computing_machine/bot/handlers/shitposter_operator.py`.
Memory DAL: `src/friendly_computing_machine/db/dal/shitposter_memory_dal.py`.
Brain job triggers: see [shitposter_brain_jobs.md](shitposter_brain_jobs.md).

## Access

Every command is a `/shitpost admin` subcommand. Callers must be in
`FCM_ADMIN_SLACK_USER_IDS` (the same admin set as `/shitpost admin optin|silence`);
anyone else gets a refusal. Every reply is ephemeral, so backer names and
snapshot text are visible only to the caller.

## Commands

| Command | Shows |
|---------|-------|
| `/shitpost admin history [n]` | Attribute and lore changes, newest first (default 20, max 100) |
| `/shitpost admin runs [n]` | Reflector runs with rejection count and reasons |
| `/shitpost admin lore [hit\|consolidated\|theme\|novel] [n]` | Active lore with each entry's source, optionally one source only |
| `/shitpost admin ideas [n]` | Idea-generator runs: status, shares, gate, entries, rejections |
| `/shitpost admin weights [n]` | Theme/novel target-weight revisions with their causes |
| `/shitpost admin retire <attribute\|lore> <id>` | Retires a current entry, cause = operator |
| `/shitpost admin snapshot <post permalink>` | Snapshot the post was written from |

Paging for long output runs from a shell with the same data:

```
fcm tools shitposter history --page 2 --page-size 20
fcm tools shitposter runs --page 1 --page-size 50
fcm tools shitposter lore --source theme --page 1
fcm tools shitposter ideas --page 1 --page-size 10
fcm tools shitposter weights --page 1
```

`--page` is 1-based; `--page-size` is 1-100. The header line reports the
range shown and the total, so the last page is recognizable.

## Reading history

Every change shows its time, operation, before/after text, the reflector run
(when one produced it), and its cause. A change with no recorded cause is not
shown: the memory foundation refuses to write one, so this only guards against
future schema drift.

Cause lines:

- **bot post**: `bot post <id>: N reactors (:emoji: xK, ...), M repliers, J negative`.
  Shows `engagement not finalized` until the post's 24h harvest window closes.
- **suggestion**: `suggestion <id> "<text>" backed by K: <@submitter>, <@backer>, ...`.
  The submitter counts once toward the threshold. Only active backers are listed,
  so a backer who withdrew after promotion no longer appears.
- **operator**: `operator slack:<user id>` for a retire by an admin.
- **fold**: a lore consolidation, with the fold run reference.
- **theme**: `theme: generalizes lore #a, #b`, the active lore the theme entry cites.
- **novel**: `novel: recent mean X < F x baseline mean Y`, the gate reading stored as
  JSON `{recent_mean, baseline_mean, fraction}` in `cause_ref`. A ref that does
  not parse is shown raw.

## Retiring an entry

`/shitpost admin retire attribute <id>` or `/shitpost admin retire lore <id>`
closes the current row and logs a retire with cause `operator` and the calling
admin's subject (`slack:<user id>`). The reply names the retired text and the
change id. A retire of an id that is not current (already retired, merged, or
never existed) is refused with the reason.

Effects on later jobs (owned by the snapshot, reflect, and write jobs):

- The entry is absent from the next snapshot.
- Older evidence does not re-add it.
- Pending drafts that depend on it are discarded.

To find an id, run `history` first; the `#<id>` on each change line is the
change log id, and the entry id is the `<id>` you pass to `retire`.

## Tracing a post

`/shitpost admin snapshot <post permalink>` takes a Slack message permalink
(`Copy link` in Slack) for a bot post and returns the snapshot id, version,
token count, and rendered text it was written from. Posts written before
snapshots were recorded report no snapshot and the latest snapshot version.

## Reading reflector runs

`/shitpost admin runs [n]` lists each run's start time, input count, carried-over
count, applied count, rejection count, and one line per rejected change with its
op and reason. Use it to see why a reflector proposal did not land.

## Reading active lore

`/shitpost admin lore [source] [n]` lists active lore ordered by source then id.
Each entry shows its id, source (`hit`, `consolidated`, `theme`, `novel`),
popularity and text. Pass a source to see only that kind.

## Reading idea-generator runs

`/shitpost admin ideas [n]` lists idea-generator runs, newest first (running
runs are not listed). Each run shows:

- status (`succeeded`, `no_op`, `skipped`, `failed`) and trigger, with a reason:
  entries written, why nothing was written, the skip reason, or the error.
- theme and novel share against target (`n/a (empty pool)` with no active lore).
- the novel gate, open or closed. A gate not yet implemented reads as closed.
- each entry written with its agent and cited lore, and each rejection with its reason.
- popularity refreshes and weight changes, `none` when empty. A weight change
  shows `source, old, new, with_mean, without_mean`; a source left alone shows
  its `unchanged_reason` (`below_minimum`, `equal_means`, `bound`, `ceiling`,
  `superseded`).

A failed run keeps whatever readings were computed before it failed, and
writes no weight changes.

## Reading source weights

Each idea-generator run first steps the theme and novel target weights, so the
new weights drive that run. Per source, finalized posts written in the trailing
window (`FCM_SHITPOSTER_WEIGHT_WINDOW_DAYS`) are split by whether their context
snapshot held that source's lore; only days with both kinds of post count. The
weight moves one step (`FCM_SHITPOSTER_WEIGHT_STEP`) toward the better-performing
group, clamped to its bounds, when each group has at least
`FCM_SHITPOSTER_WEIGHT_MIN_POSTS` posts. A step that would push theme + novel
over the combined ceiling is not applied; when both would rise and only one
fits, the weight further below its own upper bound steps.

`/shitpost admin weights [n]` lists every weight revision, newest first, with
its run and cause: the compared means, post counts and days compared, or
`initial`/`fallback` for the seeded starting weights. `/shitpost admin lore`
shows the current weights, bounds and ceiling.

## Triggering jobs

Brain jobs (harvest, reflect, write, snapshot) are triggered from the CLI, not
Slack. See [shitposter_brain_jobs.md](shitposter_brain_jobs.md) for the
`fcm workflow brain-trigger` command and the per-kind schedules.
