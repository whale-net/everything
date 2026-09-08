# Audience Score System — TOC

YouTube creator research/schedule/outcome tracking system, exposed over MCP.

## Start Here

- [`PRODUCT.md`](PRODUCT.md) — Vision, personas, load-bearing decisions
  (LB1-LB4), non-goals, and a jump table to the current-state survey,
  capability map, and milestone roadmap. Read before scoping or designing
  any ASS work.
- [`README.md`](README.md) — What this domain is, the four binaries, local
  dev (Postgres + Temporal), how to run each via `bazel run`.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — Index + the Go-throughout decision
  only; the actual sections (component map, MCP server, OAuth grants, NFR3
  interface allocation, Temporal helper, data model) live one-per-file
  under [`architecture/`](architecture/) — jump straight to the file you
  need via `ARCHITECTURE.md`'s table. NFR3 interface allocation
  (`architecture/05-nfr3-interface-allocation.md`) is what's web UI vs.
  MCP-only: six UI-only surfaces as of issue #2039 (C1/C2/C3 OAuth-consent,
  plus C18's edit slice, C20, C21), up from three.
- [`ENV.md`](ENV.md) — All environment variables: `PG_DATABASE_URL`,
  `TEMPORAL_HOST`/`TEMPORAL_NAMESPACE`/`TEMPORAL_TASK_QUEUE`, log level,
  `web`'s Google OAuth sign-in vars (`ASS_GOOGLE_CLIENT_ID` etc., C1), and
  the C2 YouTube Channel-connect scope set ("OAuth scopes" section, #1571).

## Product Docs

- [`product/01-current-state.md`](product/01-current-state.md) — What
  already exists in the repo that ASS reuses (Temporal, OAuth, MCP
  framework, Postgres/SCD2, YouTube client), and the gaps it inherits.
- [`product/02-capability-map.md`](product/02-capability-map.md) —
  Capability map, C1..C16, bucketed Now/Next/Later.
- [`product/03-roadmap.md`](product/03-roadmap.md) — Milestone definitions
  (M1, M2, M3).
