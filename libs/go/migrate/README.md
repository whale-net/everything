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
| `-bypass-version N` | `MIGRATE_BYPASS_VERSION` | `-1` (off) | Migrate directly to version N (up or down, whichever the DB needs), bypassing rollback auto-detection entirely. Self-limiting: once the DB reaches N, leaving this set is a no-op, not a live rollback trigger. |

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
- DB *ahead* of it (a rollback): fails loudly instead of silently doing
  nothing, unless one of the opt-ins above is set, in which case it migrates
  the schema down (`Runner.Migrate`) to match.

Neither opt-in is wired into the shared Helm job template by default — no
job in this repo auto-rolls-back today. A team that wants it sets
`MIGRATE_AUTO_DOWN` or `MIGRATE_BYPASS_VERSION` in their app's Helm values
`env` map (the job template already passes an arbitrary per-app env map
through to the container).

## See also

- [`MIGRATION_HISTORY.md`](MIGRATION_HISTORY.md) — history-tracking design and recovery scenarios in depth
- [`example_usage.md`](example_usage.md) — `HistoryRepo` API examples
