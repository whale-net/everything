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
| `005_version_allocation` (AR-5a) | `artifact.version_major/minor/patch` + backfill, `artifact_version_order_idx`, `version_allocation` |
| `006_reconcile_watermark` (issue #545) | `reconcile_watermark` — singleton row guarding `ReconcileApps` against a stale (older-commit) call landing after a newer one |
| `007_artifact_lifecycle` (AR-7b, **planned**) | `artifact.state`/`provenance`/`state_changed_at`, nullable `digest`/`build_id`, partial-unique digest index, `version_allocation` folded in and dropped |
| `008_app_identity_split` (AR-7c, **planned**) | append-only `app_manifest`/`chart_manifest` snapshots, `artifact.manifest_id`/`promotability`, `app`/`chart` lose their mutable metadata, `v_current_app`/`v_current_chart` |

Split this way so AR-2 needs only `001`, AR-3b adds `002`, AR-3c adds `003`,
AR-4b adds `004`, and AR-5a adds `005` — each phase ships an independently
applicable migration. `002` was originally planned to also carry `promotion`,
but AR-3b's scope is environments only (no promotion logic), so that table
moved out to its own migration in AR-3c, which needs
`environment.environment_id` to already exist as an FK target.

AR-4b and AR-5a were developed in parallel and both initially claimed `004`.
AR-4b is the lower branch in the stack, so it kept `004` and AR-5a renumbered
to `005`. Migration numbers must be unique: golang-migrate fails on a
duplicate version at **deploy** time, not build time, so a collision is
invisible to CI.

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

-- numeric ordering for "latest"/"next" -- TEXT ordering on `version` is
-- lexical and wrong ("v1.10.0" sorts before "v1.9.0"); see the AR-5
-- addendum in PLAN.md and migration 004's comments.
CREATE INDEX artifact_version_order_idx
  ON artifact (owner_id, kind, version_major DESC, version_minor DESC, version_patch DESC);

-- AllocateVersion's reservation ledger -- same collision guard shape as
-- artifact_version_idx, but on a table that doesn't require a digest/build.
CREATE UNIQUE INDEX version_allocation_idx ON version_allocation (owner_id, kind, version);
```

`reconcile_watermark` (`006`) is seeded with exactly one sentinel row at
migration time (`id = 1`, `git_sha = ''`, timestamps `0`) rather than left
empty — `SELECT ... FOR UPDATE` locks only rows it matches, so a genuinely
empty table gives two concurrent "first ever reconcile" calls nothing to
serialize against. See the migration's own comments for the full rationale
and how the sentinel row is distinguished from a "real" watermark.

`domain_adoption` gates the per-domain cutover described in
[`../ARCHITECTURE.md`](../ARCHITECTURE.md#resolved-questions): one row per
domain, with a stage of `observe` / `promote` / `allocate`. It ships in `001`
even though only AR-5 enforces it, so no domain is left without a row when the
gate turns on.

See the SCD2 section of [`../ARCHITECTURE.md`](../ARCHITECTURE.md#scd2-on-promotion)
and the repo-wide convention in `AGENTS.md`.
