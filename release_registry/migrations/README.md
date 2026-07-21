# release_registry — PostgreSQL Migrations

## Conventions

- **Naming**: `{number}_{description}.up.sql` / `{number}_{description}.down.sql` pairs.
  Numbers are zero-padded (001, 002, …) and always increase monotonically.
- **Runner**: [libs/go/migrate](../../libs/go/migrate/) — a Go CLI with automatic history tracking (`migration_history` table created by the library).
- **Applying migrations**: `bazel run //release_registry:migrate` (once a `migrate/main.go` and BUILD target are added to this repo).
  Until then, apply manually:

  ```bash
  psql "$DATABASE_URL" -f release_registry/migrations/001_create_registry_tables.up.sql
  ```

- **Rolling back**: `psql "$DATABASE_URL" -f release_registry/migrations/{n}_*.down.sql` (manual for now).

## Tables

| Table | Style | Notes |
|-------|-------|-------|
| `registry_apps` | SCD2-ish | Immutable app declarations; one row per `app_key`. |
| `registry_commits` | Append-only event log | No `valid_from`/`valid_to`; each row is a git commit record. |
| `registry_artifacts` | Append-like | Links an artifact to its version and commit. |
| `registry_promotions` | SCD2 | Uses `valid_from` / `valid_to` per AGENTS.md convention. |

See the upstream [AGENTS.md — SCD2](../../CLAUDE.md#scd2-slowly-changing-dimensions-type-2) for column conventions and index patterns.
