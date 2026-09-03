# Audience Score System

Audience Score System (ASS) helps a YouTube creator and their analyst turn a
hunch into a scheduled, published video, and then tells them whether they
were right. See [`PRODUCT.md`](PRODUCT.md) for the full vision, personas,
and load-bearing decisions.

Language/runtime is **Go throughout** — the web app, the MCP server, and the
Temporal worker all reuse `//libs/go/temporal`, `//libs/go/db`, and
`//libs/go/migrate`. See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the
component map and why this domain doesn't follow the FastMCP/Python MCP
server precedent used elsewhere in the repo.

## Binaries

| Binary | `release_app` name | app_type | Status |
|--------|---------------------|----------|--------|
| `migrate/` | `migration` | `job` | Migration 001 (identity core, #1568) applied |
| `web/` | `web` | `external-api` | Scaffold only (#1570): binary + health endpoint + UI shell build; Google OAuth sign-in/sign-up not yet implemented |
| `mcp/` | `mcp` | `external-api` | Not yet built |
| `worker/` | `worker` | `worker` | Not yet built |

All four share the `audience-score-system` `release_app` domain, producing
images `audience-score-system-migration`, `-web`, `-mcp`, `-worker`.

## Local Development

Requires:

- **Postgres** — connection string in `PG_DATABASE_URL` (see
  [`ENV.md`](ENV.md)). Access is via `//libs/go/db`.
- **Temporal** — server address in `TEMPORAL_HOST` (see `//libs/go/temporal`
  and [`ENV.md`](ENV.md)). Used by the `worker` binary for per-Channel sync.

Once each binary exists:

```bash
# Apply pending migrations (job; run before the other three)
bazel run //audience_score_system/migrate:migration

# Run the web UI (OAuth signup/login, Channel connect, invite/accept,
# schedule approval — the only four UI surfaces, per NFR3)
bazel run //audience_score_system/web

# Run the MCP server (every other capability)
bazel run //audience_score_system/mcp

# Run the Temporal worker (per-Channel sync)
bazel run //audience_score_system/worker
```

Today `migrate/` and `web/` exist (`web` is scaffold-only — sign-in isn't
implemented yet, see the table above):

```bash
bazel run //audience_score_system/migrate:migration
bazel run //audience_score_system/migrate:migration -- -version
bazel run //audience_score_system/migrate:migration -- -history

bazel run //audience_score_system/web
```

See [`ENV.md`](ENV.md) for the full environment variable list and
[`libs/go/migrate/README.md`](../libs/go/migrate/README.md) for the runner's
CLI flags.
