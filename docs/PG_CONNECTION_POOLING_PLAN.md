# Postgres Connection Pooling — Remediation Plan

Tracks: [#1022](https://github.com/whale-net/everything/issues/1022) — "review pg connection pooling" (`type:spike`). Original report: *"exhausted connections. need pooling. not sure if current usage patterns support it."*

## Summary

Every service in the repo that talks to Postgres already pools connections client-side (`pgxpool` in Go, SQLAlchemy in Python) — the problem isn't that pooling is missing, it's that **no pool has an explicit ceiling**, and Python has **two independent, uncoordinated implementations** of "create a Postgres engine" with different defaults. Nothing in the repo aggregates how many connections the fleet can open against the shared instance at once. That's fixable without new infrastructure. A PgBouncer-style shared pooler is not ruled out, but current usage patterns (no advisory locks, no `LISTEN`/`NOTIFY`, no temp tables — verified below) mean it's a viable *second* lever, not a required one, and it isn't a substitute for step 1.

## Current State

### Go — one shared lib, already consolidated, zero tuning

All 6 Go processes that touch Postgres funnel through the same shared library, `libs/go/db` (`db.NewPool` → `pgxpool.New`), confirmed by direct grep of every call site:

| Process | Domain | Replicas |
|---|---|---|
| `manmanv2/api` | manmanv2 | 2 |
| `manmanv2/log-processor` | manmanv2 | 1 |
| `manmanv2/ui` | manmanv2 | 1 |
| `app-registry-api` (server) | app_registry | — |
| `app-registry-worker` (Temporal worker + outbox drainer + artifact reaper, one shared pool) | app_registry | 1 |
| `app-registry-ui` (session store only) | app_registry | — |

`libs/go/db/db.go` sets **no explicit pool tuning at all** — no `MaxConns`, `MinConns`, `MaxConnLifetime`, or `MaxConnIdleTime`. That means every process falls back to pgx's built-in default: `MaxConns = max(4, runtime.NumCPU())`. Critically, `runtime.NumCPU()` reads the **node's** core count, not the pod's cgroup CPU limit — a pod with a 250m CPU limit scheduled on a 32-core node can still open up to 32 connections, and the pool never shrinks (no idle/lifetime eviction). Replica counts here are low (mostly 1, one at 2), so the exhaustion driver isn't fan-out — it's that nothing bounds any individual process, and the bound that exists (`NumCPU()`) is disconnected from what the pod is actually entitled to.

The migration runner (`libs/go/migrate/cli.go`) uses a separate one-shot `sql.Open("pgx", dsn)` — short-lived, lower risk, out of scope for pooling tuning but should still `Close()` cleanly (verify in Phase 1).

### Python — two divergent, uncoordinated implementations

Unlike Go, Python has **no single consolidated entry point** — this is the direct "re-implementation instead of reuse" problem:

1. **`libs/python/postgres/engine.py`** (`create_engine`) — good, deliberate defaults: `pool_size=20`, `max_overflow=30` (50 connections/process), `pool_recycle=3600`, all env-overridable (`SQLALCHEMY_POOL_SIZE` etc.), explicitly documented as tuned for *"production FastAPI applications with multiple gunicorn workers."* Currently used only by the legacy `manman/` (V1, maintenance mode).
2. **`libs/python/cli/providers/postgres/postgres.py`** (`create_postgres_context`) — a second, independent factory that calls raw `sqlalchemy.create_engine()` directly. No pool-size parameters are exposed at all (SQLAlchemy's bare defaults apply: `pool_size=5`, `max_overflow=10` → 15/process), and it hardcodes `pool_recycle=60`. This is what `friendly_computing_machine` — the active Slack bot, which runs several independent long-lived processes (`bot_cli`, `subscribe_cli`, `workflow_cli`, each opening its own engine) — actually uses in production.

Two libraries, two different default philosophies, neither one chosen deliberately for the workload it ends up serving (`libs/python/postgres`'s FastAPI-oriented 50-connection default was never applied where FCM's CLI-process pattern needed something smaller and explicit; `libs/python/cli`'s bare SQLAlchemy defaults were never routed through the lib that already solved this). Neither is listed in `libs/TOC.md` — both are undocumented, which is likely why the fork happened in the first place.

leaflab does not touch Postgres directly (no `psycopg`/`asyncpg`/`sqlalchemy` usage found there).

### Infra

- No PgBouncer or other pooler exists anywhere in the repo today (checked all YAML/Helm/compose/Tiltfiles).
- Postgres itself is externally managed — there's no IaC or Helm chart for the Postgres server in this repo, so actual `max_connections` isn't visible from source. Live `pg_stat_activity` / `max_connections` checks were attempted via the App Registry Postgres MCP tools during this investigation: the prod tool was correctly auto-blocked for a background/unattended session (expected — prod is gated), and the dev tool errored (`SSL error: unexpected eof` — the dev Tilt environment isn't currently up). **Getting real numbers here is Phase 0, not optional** — see below.
- Locally, Tilt provisions Postgres per-domain (`tools/tilt/common.tilt`'s `setup_postgres()`), so each domain gets its own dev instance. Whether prod mirrors that (one instance per domain) or centralizes onto one shared instance across manmanv2/app_registry/friendly_computing_machine is **unconfirmed** and materially changes how urgent this is — confirm as part of Phase 0.
- Usage-pattern check for pooler compatibility: grepped the entire repo for `pg_advisory_lock`/`pg_try_advisory`, `LISTEN`/`pg_notify`, and `CREATE TEMP[ORARY]` — **zero hits**. Nothing here relies on session-pinned Postgres features. This directly answers the issue's "not sure if current usage patterns support it": they do, with one caveat (pgx prepared-statement caching — see Phase 4).

## Root Cause

Not "no pooling" — **no ceiling, and no coordination**. Total possible concurrent connections against the shared instance scales with `(process count) × (per-process default, which for Go tracks host CPU count rather than pod limits, and for Python is one of two uncoordinated values)`, and nothing in the repo has ever computed or bounded that sum against Postgres's actual `max_connections`.

## Plan

### Phase 0 — Get real numbers (prerequisite, do first)

Before changing any default, confirm against a live environment:
- Actual `max_connections` on the (dev and, separately, prod) Postgres instance(s), and current peak concurrent connections (`pg_stat_activity`) — use the already-installed `app-registry-pg-dev` / `app-registry-pg-prod` MCP tools (`analyze_db_health(health_type="connection")`) once the dev Tilt environment is up; prod access requires an attended session (auto-blocked here).
- Whether manmanv2, app_registry, and friendly_computing_machine share one Postgres instance in prod or have separate ones — changes whether pool budgets need to be coordinated across domains or just within one.

This determines whether Phase 1+2 (client-side caps) alone is sufficient, or whether Phase 4 (PgBouncer) is actually needed.

### Phase 1 — Go: add explicit, conservative pool ceilings to `libs/go/db`

Single-file change with fleet-wide effect, since every Go process already funnels through it:
- Extend `db.NewPool` to build a `pgxpool.Config` (via `pgxpool.ParseConfig`) and set `MaxConns`, `MinConns`, `MaxConnLifetime`, `MaxConnIdleTime` explicitly instead of leaving them at pgx defaults.
- Defaults should be small and deliberate for this fleet's actual shape (low-replica singleton-ish services), not derived from `NumCPU()` — e.g. `MaxConns` default 5–10, `MaxConnIdleTime` ~5m, `MaxConnLifetime` ~30–60m — all overridable via env (`PG_POOL_MAX_CONNS`, `PG_POOL_MIN_CONNS`, `PG_POOL_MAX_CONN_LIFETIME`, `PG_POOL_MAX_CONN_IDLE_TIME`), following the same override pattern `libs/python/postgres` already uses.
- Audit that every entrypoint calls `pool.Close()` on shutdown (quick check, `app-registry-worker`'s 3 concurrent loops already share one pool — confirm the other 5 processes do too, no changes expected here, just verification).
- Update `libs/go/db/README.md` with the new env vars.

### Phase 2 — Python: consolidate onto one shared lib, then tune it

- Change `libs/python/cli/providers/postgres/postgres.py::create_postgres_context` to delegate to `libs/python/postgres.create_engine` instead of calling `sqlalchemy.create_engine` directly, passing through `echo`/`pool_pre_ping`/`pool_recycle` and forwarding `engine_initializer`. This removes the second implementation entirely — one Python Postgres entry point, mirroring what Go already has.
- Re-evaluate `libs/python/postgres`'s defaults now that its actual consumers include FCM's CLI-process pattern, not just hypothetical FastAPI+gunicorn: the current `pool_size=20/max_overflow=30` (50/process) default is oversized for a fleet of low-replica singleton processes. Lower the *default* (env-overridable, as today) to something in the 5–10 / 5–10 range, matching Phase 1's Go sizing philosophy — high-concurrency services can still opt up via `SQLALCHEMY_POOL_SIZE`.
- Add `libs/go/db` and `libs/python/postgres` to `libs/TOC.md` (both are currently undocumented, which is a likely contributor to the fork in the first place).

### Phase 3 — Verify Phase 0's numbers against Phase 1+2's new caps

Once Phase 0's real `max_connections` and process/replica inventory are in hand, sanity-check `Σ(process pool caps)` against it with headroom (e.g. target ≤60% of `max_connections` to leave room for migrations, ad hoc `psql`, and burst). If it fits, Phase 4 is unnecessary — close the issue.

### Phase 4 — PgBouncer (only if Phase 3 shows client-side caps aren't enough)

Not scaffolded yet since Phase 0–3 may make it moot, but if needed:
- Deploy in **transaction pooling mode** (safe here — confirmed no advisory locks, `LISTEN`/`NOTIFY`, or temp-table usage anywhere in the repo).
- **pgx caveat**: pgx caches prepared statements by default, which breaks under transaction-mode PgBouncer (a prepared statement can get executed on a different backend than the one that prepared it). Add an opt-in `PG_USE_PGBOUNCER` toggle to `libs/go/db` that sets `pgxpool.Config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol` — a one-line flip per service, not a per-callsite rewrite, keeping the "common lib" property intact.
- **SQLAlchemy caveat**: avoid "double pooling" — when a client sits behind PgBouncer, shrink (or `NullPool`) the client-side pool so PgBouncer is the actual pool, not an extra buffering layer in front of it.
- **Migrations bypass PgBouncer**: `libs/go/migrate` and `libs/python/alembic` must keep a *direct* (non-pooled, session-mode) connection string — `CREATE INDEX CONCURRENTLY` and migration-tool advisory locks (Alembic/most Go migrators use one to serialize concurrent migration runs) require session affinity that transaction-mode pooling breaks.

## Non-Goals

- Rearchitecting any service's query/transaction patterns — the investigation found no long-held transactions, session-pinned features, or obviously pathological query patterns; this plan is about *pool sizing and coordination*, not query-level changes.
- Standing up IaC/Helm for the Postgres server itself — out of scope, and per `AGENTS.md`, production is not patched directly by an agent regardless.

## Testing

- Go: unit test that `db.NewPool` honors `PG_POOL_MAX_CONNS` etc.; existing `manmanv2`/`app_registry` Postgres-integration test infra (see #519, #616) covers behavioral regressions.
- Python: extend `libs/python/postgres/engine_test.py` for the same env-override behavior; add a regression test that `create_postgres_context` now produces an engine with the shared lib's pool settings, not bare SQLAlchemy defaults.
- No behavior change expected for existing call sites beyond pool sizing — no schema/query changes.

## Open Questions (surface to a human before Phase 4)

1. Does prod centralize manmanv2 + app_registry + friendly_computing_machine on one Postgres instance, or are they separate? (Phase 0)
2. What is the actual prod `max_connections` and current peak usage? (Phase 0, requires attended session — prod DB access is auto-blocked for background sessions)
3. Given Phase 0's answer, is Phase 4 (PgBouncer) needed at all, or does Phase 1+2 alone give enough headroom?
