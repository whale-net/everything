# migrate

Shared Go library + CLI (`migrate.RunCLI`) for running Postgres schema
migrations, wrapping `github.com/golang-migrate/migrate/v4` with history
tracking and rollback-safe defaults. Embedded into per-domain binaries:
`manmanv2/migrate` (`control-migration`), `tools/app_registry/migrate`, and
`leaflab/migrate`. Each binary is deployed as a Helm `job`-type app, run as a
pre-install/pre-upgrade hook ahead of the domain's other apps.

## Embedding

```go
//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	migrate.RunCLI(migrations, "migrations")
}
```

## CLI flags / env vars

| Flag | Env var | Default | Description |
|---|---|---|---|
| _(none — default action)_ | | | Reconciles the schema to this binary's latest embedded migration. See "Rollback detection" below for what happens when the DB is ahead of it. |
| `-down` | | `false` | Roll back ALL migrations to version 0. |
| `-steps N` | | `0` | Run N migrations (positive=up, negative=down). |
| `-version` | | `false` | Print current migration version and exit. |
| `-force N` | | `-1` (off) | Force-set the migration version without running SQL (recovery). Validated against history unless `-force-dangerous` is also set. |
| `-force-dangerous` | | `false` | Skip history validation when forcing (dangerous). |
| `-history` | | `false` | Print the migration history table. |
| `-history-limit N` | | `20` | Number of history rows to print with `-history`. |
| `-tracked` | | `true` | Use per-step history tracking on the default up path (`UpWithTracking`) instead of plain `Up()`. |
| `-auto-down` | `MIGRATE_AUTO_DOWN` | `false` | Allow the default path to automatically migrate DOWN when the DB is ahead of this binary's latest migration (e.g. an older image re-running after a rollback). **A standing switch** — leave it set and every future mismatch auto-rolls-back, not just the one you meant to fix. |
| `-bypass-version N` | `MIGRATE_BYPASS_VERSION` | `-1` (off) | Operator-approved ceiling, not a target: if the DB is ahead of this binary's latest migration but at or below N, leave the schema as-is — no migration runs at all. For additive-only migrations (e.g. a new column an older binary just ignores) where the extra state is safe to keep. Only affects the "DB is ahead" case; never blocks a normal forward migration. |

`PG_DATABASE_URL`, if set, overrides the individual `DB_HOST`/`DB_PORT`/
`DB_USER`/`DB_PASSWORD`/`DB_NAME`/`DB_SSL_MODE` vars read by
`DefaultConfig()`.

## Rollback detection

Re-running the migration job with an older image — as happens on an
ArgoCD/Helm rollback — used to silently no-op: `Up()` found nothing newer to
apply and exited successfully, leaving the DB on the *newer* schema the
older, rolled-back code doesn't expect (issue #883).

The default path now compares the DB's current version against
`Runner.LatestVersion()` (the highest version embedded in this binary):

- DB behind or at that version: runs up as before (`UpWithTracking`/`Up`).
- DB *ahead* of it (a rollback), checked in this order:
  1. **`MIGRATE_BYPASS_VERSION`/`-bypass-version` covers it** (current version
     ≤ the configured ceiling): do nothing. No migration runs, up or down —
     the schema is left exactly as it is. Use this when the only thing ahead
     is an additive, backward-compatible migration (e.g. a new column the
     older binary never references) and rolling it back would just throw
     away data the newer app already wrote.
  2. **`MIGRATE_AUTO_DOWN`/`-auto-down` is set**: migrate the schema down
     (`Runner.Migrate`) to this binary's latest known version.
  3. **Neither is set**: fail loudly. Nothing is touched.

Both opt-ins only change behavior in the "DB is ahead" branch — a normal
forward deploy always just runs up, regardless of either setting.

Neither opt-in is wired into the shared Helm job template by default — no
job in this repo auto-rolls-back or auto-bypasses today. A team that wants
one sets `MIGRATE_AUTO_DOWN` or `MIGRATE_BYPASS_VERSION` in their app's Helm
values `env` map (the job template already passes an arbitrary per-app env
map through to the container).

## Dropping tables: wait-3-then-drop

Never `DROP TABLE` in a single migration. A hard drop is a physical delete —
if a rollback lands after it (an older image redeployed via
`-auto-down`/`MIGRATE_AUTO_DOWN`, see "Rollback detection" above, or a manual
`-down`/`-steps`), there is no data left for the down migration to restore.
Split any table removal into two migrations, at least **3 releases apart**:

1. **Soft-drop migration** (`NNN_drop_<table>.up.sql`): quarantine the table
   without losing its data.
   - Drop every foreign key that references the table, and every foreign key
     the table itself declares.
   - Drop every other constraint (`CHECK`, `UNIQUE`, `PRIMARY KEY` included)
     and every index on the table.
   - `ALTER TABLE <table> RENAME TO __<table>;` — the `__` prefix marks it as
     quarantined and pending a hard drop. It's grep-able across the repo
     (`grep -rn "RENAME TO __" **/migrations`) to find every table currently
     in its wait window.
   - The paired `.down.sql` reverses this exactly: rename `__<table>` back to
     `<table>`, then recreate the dropped constraints and indexes. This is
     what `-auto-down` replays if a rollback happens during the wait window —
     the data is untouched, so it's a full, safe restore.
   - Record the earliest hard-drop release/date in a comment at the top of
     the migration (there's no automated release counter) so the follow-up
     migration isn't written too early.

2. **Hard-drop migration** (`NNN_drop_<table>_final.up.sql`), added no sooner
   than 3 releases after the soft-drop shipped and has been live in
   production for that long with no rollback: `DROP TABLE __<table>;`. From
   this point on there is no way back — say so in a comment, and make the
   `.down.sql` a no-op that documents the data is gone rather than pretending
   to restore it.

The 3-release wait gives operators a window to notice something still
depends on the table (or that the drop was a mistake) and roll back to the
soft-drop's `.down.sql` while the data still exists, before it's actually
discarded.

## See also

- [`MIGRATION_HISTORY.md`](MIGRATION_HISTORY.md) — history-tracking design and recovery scenarios in depth
- [`example_usage.md`](example_usage.md) — `HistoryRepo` API examples
