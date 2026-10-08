# Shitposter brain: operator runbook

Operator guide for the Shitposter persona's memory: reading what changed and
why, reading reflector runs, retiring entries, and tracing a post to the
context snapshot it was written from. Admin-only; every reply is ephemeral.

Code: `src/friendly_computing_machine/bot/handlers/shitposter_operator.py`.
Memory DAL: `src/friendly_computing_machine/db/dal/shitposter_memory_dal.py`.
Brain job triggers: see [shitposter_brain_jobs.md](shitposter_brain_jobs.md).

## Commands

| Command | Shows |
|---------|-------|
| `/shitposter history [n]` | Attribute and lore changes, newest first |
| `/shitposter runs [n]` | Reflector runs with rejection count and reasons |
| `/shitposter retire <attribute\|lore> <id>` | Retires an active entry, cause = operator |
| `/shitposter snapshot <post permalink>` | Snapshot id the post was written from |

Paging for long output is available through `tools_cli` with the same scopes.

## Reading history

Every change shows its time, operation, before/after text, and cause. A change
with no recorded cause is not shown: the memory foundation refuses to write one.

Cause kinds:

- **post**: a bot post, with its engagement (reactions, replies).
- **suggestion**: a promoted suggestion, with its backers. Backer names are
  visible to the operator only, in the ephemeral reply.
- **operator**: a retire by an admin, with the admin's subject.

## Retiring an entry

`/shitposter retire attribute <id>` or `/shitposter retire lore <id>` closes the
current row and logs a retire with cause `operator`. Effects on later jobs:

- The entry is absent from the next snapshot.
- Older evidence does not re-add it.
- Pending drafts that depend on it are discarded.

## Tracing a post

`/shitposter snapshot <post permalink>` returns the snapshot id the post was
written from. A post written before snapshots were recorded reports no snapshot.

## Reading reflector runs

`/shitposter runs [n]` lists each run's time, rejection count, and the reasons
for each rejection. Use it to see why a reflector proposal did not land.
