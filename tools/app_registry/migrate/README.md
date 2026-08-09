# app-registry-migration

Schema migration runner for the App Registry database. Built in **AR-1**.

Not yet implemented.

## Pattern

Follows `manmanv2/migrate` in spirit — embedded SQL plus `libs/go/migrate` —
with one deviation: the `//go:embed` lives in a small `schema` sub-package
(`schema.Migrations` / `schema.Dir`) rather than directly in `main.go`, so the
Postgres integration tests under `server/repository/postgres` can apply the
same real migrations instead of duplicating the SQL as hand-written DDL.

```go
// migrate/schema/schema.go
//go:embed migrations/*.sql
var Migrations embed.FS
const Dir = "migrations"

// migrate/main.go
func main() { migrate.RunCLI(schema.Migrations, schema.Dir) }
```

Deployed as a Helm pre-sync job (`app_type: job`), ordered ahead of the API via
ArgoCD sync waves. See `friendly_computing_machine/docs/argocd-integration.md`.

## Planned migrations

| File | Contents |
|---|---|
| `001_initial_schema` | `app`, `chart`, `chart_app`, `build`, `artifact`, `artifact_link`, `idempotency_key`, `domain_adoption` |
| `002_environment_registry` | `environment`, seeded with `dev`/`stage`/`prod` |
| `003_promotion` (AR-3c) | `promotion` (SCD2), `promotion_event`, `v_current_promotion` |
| `004_writeback_outbox` (AR-4b) | `writeback_outbox` |

Split this way so AR-2 needs only `001`, AR-3b adds `002`, AR-3c adds `003`,
and AR-4b adds `004` — each phase ships an independently applicable
migration. `002` was originally planned to also carry `promotion`, but
AR-3b's scope is environments only (no promotion logic), so that table moved
out to its own migration in AR-3c, which needs `environment.environment_id`
to already exist as an FK target.

## Required indexes

Do not omit these; they are load-bearing, not optimizations.

```sql
-- makes double-promotion structurally impossible, and is the hot read path
CREATE UNIQUE INDEX promotion_current_idx
  ON promotion (environment_id, target_key)
  WHERE valid_to IS NULL;

-- historical "state at time T" queries
CREATE INDEX promotion_window_idx
  ON promotion (environment_id, target_key, valid_from DESC);

-- digest is the real artifact identity
CREATE UNIQUE INDEX artifact_digest_idx ON artifact (digest);

-- version allocation collision guard (AR-5 depends on this)
CREATE UNIQUE INDEX artifact_version_idx ON artifact (owner_id, kind, version);
```

`domain_adoption` gates the per-domain cutover described in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md#resolved-questions): one row per
domain, with a stage of `observe` / `promote` / `allocate`. It ships in `001`
even though only AR-5 enforces it, so no domain is left without a row when the
gate turns on.

See the SCD2 section of [`../ARCHITECTURE.md`](../ARCHITECTURE.md#scd2-on-promotion)
and the repo-wide convention in `AGENTS.md`.
