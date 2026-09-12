# krill — Architecture

This document covers what exists after this task (issue #2487, M1 domain
scaffolding). See [`PRODUCT.md`](PRODUCT.md) for vision, personas,
load-bearing decisions, and the milestone roadmap that drives what gets
built next.

## Component map (as of this task)

```
                 ┌───────────┐
   Postgres  ◄───│  migrate  │  job: applies schema, seeds `scope` (LB1/NFR2)
   (scope)        └───────────┘
        ▲
        │
   ┌────┴────┐
   │   api   │  external-api: /healthz (live DB ping) only, no spec endpoints
   └─────────┘
```

`migrate` and `api` each get their own Postgres connection
(`PG_DATABASE_URL`, `//libs/go/db` / `//libs/go/migrate` — see `ENV.md`).
There is no `mcp` binary yet; `krill/plugin/` exists as a placeholder
directory for its future Claude Code plugin entries (see README.md
"Claude Code plugin"), and `//krill:krill_chart` only bundles `migrate`
and `api` today.

## The `scope` table (LB1)

Every entity table this milestone (and every later one) adds carries a
non-null `scope_id` foreign key — including this task, which seeds
`scope` before any entity table exists. `scope`, not any entity row, owns
this repo's forge coordinates:

- `repo_full_name` (e.g. `whale-net/everything`)
- `default_branch` (e.g. `main`)
- `pointer_issue_number` (nullable — unset until a later task's FR20
  pointer-artifact issue exists)

**Why the indirection, not a bare `repo_full_name` column on every
table:** with a natural key, a repo rename is a rewrite of every row that
carries it; with a surrogate `scope_id`, it is a single-row edit on
`scope`. It also gives a later cross-product decision (a `Later` capability
in `PRODUCT.md`) somewhere to live without a migration. M1 seeds exactly
one `scope` row (`krill/migrate/seed`) and exposes no CRUD over it — the
scope selector, per-scope authorization, and whether a scope is a repo or
an org are all deferred to whenever those capabilities are actually
scoped.

**Mutation-shape boundary call (LB3):** `scope` is a plain mutable config
row — not SCD2 (`AGENTS.md` "SCD2" — no `valid_from`/`valid_to`) and not
the work axis's append-only + claimed shape. See the schema comment in
`migrate/schema/migrations/001_scope.up.sql` for the same reasoning
in-line with the DDL it applies to.

## Migration numbering (M1)

Assigned up front in issue #2487 so parallel tasks under the same
milestone never collide on a migration version:

| Version | Contents | Task |
|---------|----------|------|
| `001` | `scope` | This task (#2487) |
| `002` | Spec entities (Product/FeatureSet/Feature/FR/NFR/LoadBearingDecision) | Later M1 task |
| `003` | `session` (FR3's `init` gate) | Later M1 task |
| `004` | Milestone reference + association (FR17) | Later M1 task |
| `005` | Pointer artifact (FR20) | Later M1 task |

## Open items

- No spec entity model yet (Product/FeatureSet/Feature/FR/NFR) — `api`
  exposes nothing beyond `/healthz`.
- No MCP surface yet — `krill/plugin/` is a placeholder only.
- No auth (NFR1's two-front-door pattern) wired up yet — `api` has no
  authenticated route to gate.
