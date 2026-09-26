# Deploy ordering, rollback, and recovery

Read this if you have deployed the manman V1 removal release and something is wrong. The short
version: **`helm rollback` is not a way back.** It turns a partial outage into a total one. The
recovery paths are at the bottom.

## Deploy ordering is correct — do not spend time on it

The migration Job is a Helm hook annotated `pre-install,pre-upgrade` at weight `-5`; the ServiceAccount
it references is a hook at weight `-10`; and every app container passes `--skip-migration-check`,
because the Job — not the app — owns schema changes. So schema changes land before the new pods come
up, as intended. If you are reading this wondering whether migrations race the app rollout: they do
not.

## The `manmanstatusupdate` drop is a one-way door

The manman V1 relay's `manmanstatusupdate` table was dropped by forward migration
`a3e7d51c9b42` (`2026_09_25_1015-a3e7d51c9b42_drop_manmanstatusupdate.py`). Three facts make this
irreversible:

1. **Applied migrations are never edited.** The migration that creates the table and the migration
   that recreated it both stay byte-for-byte untouched; the drop is a new revision on top. Reverting
   a commit does not bring the table back.
2. **The migration has no working downgrade by design.** Its `downgrade()` raises
   `NotImplementedError` rather than fabricating an empty table, because the rows it held cannot be
   reconstructed from what is left in the schema.
3. **There is no `pre-rollback` hook counterpart.** Nothing runs on the way back down, and none can
   be added: a rollback hook would have to recreate a table whose data-dependent contents it does
   not know.

## What `helm rollback` actually gets you

After this release, a `helm rollback` brings back a **pre-M1** `bot` image running against a database
in which `manmanstatusupdate` no longer exists. The pre-M1 bot expects that table, so the surviving
app cannot start against its own schema. A partial outage — the dropped V1 feature — becomes a total
one.

It is worse than that, and the difference matters if you are deciding under pressure. **M1 deletes
the `subscribe` release app outright rather than deprecating it.** The pre-M1 release composed a
`subscribe` image; the current chart has no entry for it, so a restored `subscribe` image has no
arguments and its pod does not serve the V1 subscribe service correctly.

So: **a rollback is not a clean restoration of the pre-M1 state.** Do not describe it to anyone as
"rolling back to the previous release" as though that returned you to a known-good configuration. It
returns you to a release that cannot run against this database and cannot compose into this chart.

## Recovery procedure

Pick one of these. Both are forward moves; there is no downward move.

### Option A — restore the database to a pre-M1 backup

The reliable option, and the right one if the bot's own schema is the problem.

1. Take a snapshot of the current (broken) database first, so you can inspect what you were looking
   at.
2. Restore the most recent pre-M1 backup over it.
3. Roll forward the images that match that database state — i.e. the pre-M1 release — so app and
   schema agree.
4. Confirm the bot starts and the Slack socket connects before declaring the incident closed.

This gives you a consistent, known-good pair again. It costs you everything written since the
migration, so weigh that against Option B.

### Option B — apply a repair forward migration

Faster and keeps post-migration data, at the cost of hand-writing schema under pressure.

1. Write a new Alembic revision on top of the current head that restores whatever the failing app
   actually needs (e.g. re-creates `manmanstatusupdate` with an empty body).
2. Ship it through the normal migration path — the same `pre-install,pre-upgrade` hook the release
   already uses. Do not hand-run DDL against production; nothing here runs it for you otherwise.
3. Only re-create a table if you have confirmed the running app genuinely fails without it. The
   original rows are gone and an empty table will not satisfy code that expects data.

### Not in scope

No rollback automation exists or is planned: there is no `pre-rollback` hook, and adding one is not
possible for a data-dependent drop. Do not spend an incident looking for one.
