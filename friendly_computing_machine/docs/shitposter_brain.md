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
| `/shitpost admin retire <attribute\|lore> <id>` | Retires a current entry, cause = operator |
| `/shitpost admin snapshot <post permalink>` | Snapshot the post was written from |

Paging for long output runs from a shell with the same data:

```
fcm tools shitposter history --page 2 --page-size 20
fcm tools shitposter runs --page 1 --page-size 50
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

## Triggering jobs

Brain jobs (harvest, reflect, write, snapshot) are triggered from the CLI, not
Slack. See [shitposter_brain_jobs.md](shitposter_brain_jobs.md) for the
`fcm workflow brain-trigger` command and the per-kind schedules.
