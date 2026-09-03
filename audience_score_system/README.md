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

| Binary | `release_app` name | app_type | Responsibility |
|--------|---------------------|----------|--------|
| `migrate/` | `migration` | `job` | Applies every migration (001-005: identity, research/schedule/outcome, web session, channel credentials, MCP credentials). |
| `web/` | `web` | `external-api` | The only UI surface. Three of its surfaces are UI-only (NFR3): Google OAuth sign-in/sign-up (C1), Channel connect (C2), Analyst invite/accept (C3). Its fourth, schedule-draft approve/un-approve/edit (C8), is also reachable via `mcp` (issue #1648) -- both are equally-capable front ends onto the same store. |
| `mcp/` | `mcp` | `external-api` | Every other capability (C4-C7, C8, C9, C10) as MCP tools -- research notes, viability verdicts, schedule sync reads, pacing policy, schedule drafting/commit/un-commit/edit, outcome match confirm/reject, and browsing. |
| `worker/` | `worker` | `worker` | Per-Channel Temporal scheduled workflow: syncs YouTube schedule (C6) and published-video metrics (C9) on a ~1-6 hour cadence (NFR4, default 3h). A manual run can be forced via `mcp`'s `trigger_channel_sync` tool. |

All four are complete as of M1 (see [`PRODUCT.md`](PRODUCT.md)'s roadmap)
and share the `audience-score-system` `release_app` domain, producing
images `audience-score-system-migration`, `-web`, `-mcp`, `-worker`. See
`//audience_score_system/citest` for the milestone's own end-to-end
integration test, which drives all four together.

## Local Development

Requires:

- **Postgres** — connection string in `PG_DATABASE_URL` (see
  [`ENV.md`](ENV.md)). Access is via `//libs/go/db`.
- **Temporal** — server address in `TEMPORAL_HOST` (see `//libs/go/temporal`
  and [`ENV.md`](ENV.md)). Used by the `worker` binary for per-Channel sync.
- **A Google OAuth2 client** (Google Cloud Console → APIs & Services →
  Credentials → "OAuth client ID", type "Web application") for C1
  sign-in and C2 Channel-connect — both grants reuse the same client
  ID/secret, just different redirect paths (see [`ARCHITECTURE.md`](ARCHITECTURE.md)
  "OAuth grants"). Add two authorized redirect URIs under
  `ASS_OAUTH_REDIRECT_BASE_URL`: `/oauth/google/callback` and
  `/oauth/youtube/callback`. Set `ASS_GOOGLE_CLIENT_ID`/
  `ASS_GOOGLE_CLIENT_SECRET` from that client, plus `ASS_SESSION_SECRET`
  and `ASS_TOKEN_ENCRYPTION_KEY` to any local secret values (see
  [`ENV.md`](ENV.md) for every variable each binary reads).

Apply migrations once, then run each binary (each `bazel run` blocks in
the foreground; use separate terminals):

```bash
# 1. Apply pending migrations (job; run before the other three)
bazel run //audience_score_system/migrate:migration
bazel run //audience_score_system/migrate:migration -- -version   # check current version
bazel run //audience_score_system/migrate:migration -- -history   # applied-migration history

# 2. Run the web UI (OAuth signup/login, Channel connect, invite/accept --
#    its three UI-only surfaces, per NFR3 -- plus a schedule approve/
#    un-approve/edit UI that mcp's schedule-draft tools also cover)
bazel run //audience_score_system/web

# 3. Run the MCP server (every other capability -- C4-C7, C8, C9, C10)
bazel run //audience_score_system/mcp

# 4. Run the Temporal worker (per-Channel schedule/outcome sync)
bazel run //audience_score_system/worker
```

See [`ENV.md`](ENV.md) for the full environment variable list and
[`libs/go/migrate/README.md`](../libs/go/migrate/README.md) for the runner's
CLI flags.

### Pointing an MCP client at the server

`mcp` speaks the MCP streamable-HTTP transport (`ASS_MCP_ADDR`, default
`:8081`) and authenticates every call via a bearer credential (see
[`ARCHITECTURE.md`](ARCHITECTURE.md) "MCP server: caller authentication") --
there is no separate MCP-specific client-secret variable.

1. Sign in to `web` (`/login`) as the Person you want the MCP client to
   act as.
2. Mint a credential for that Person via `store.CredentialStore.Mint`
   (`mcp_credential`, migration 005) -- exposing this as a `web` endpoint
   is filed as a scope note (see issue #1591); until then, mint one
   directly against the running Postgres, e.g. via a short Go snippet or
   `psql` calling the same store method a future endpoint would.
3. Configure the MCP client (Claude, Copilot, opencode, Antigravity, or
   any other MCP-capable agent) with:
   - **Endpoint**: `http://localhost:8081` (or `ASS_MCP_ADDR`'s address)
   - **Transport**: streamable HTTP
   - **Authorization header**: `Bearer <the minted token>`
4. Call `whoami` first to confirm the credential resolves to the expected
   Person, then any Channel-scoped tool (e.g. `list_research_notes`,
   `get_channel_schedule`) with that Person's `channel_id`.

`//audience_score_system/citest`'s end-to-end test
(`e2e_test.go`) does exactly this against a real, in-process `mcp.Server`
via `mcp.NewClient` + `StreamableClientTransport` -- read it for a
working, minimal reference client if wiring up a new MCP client
integration.

### Claude plugin: agents and skills for the three loops

`plugin/user/` (marketplace name `audience-score-system`) registers the
dev/prod MCP endpoints above as a Claude Code plugin, plus two agent
personas and three skills that drive the product's three loops
end-to-end without a human hand-authoring every tool call:

- **Agents** (`plugin/user/agents/`): `researcher` gathers cited research
  notes (C4); `analyst` makes every judgment call the loops require --
  viability verdicts (C5), schedule-draft proposals (C7), and
  outcome-match resolution (C9) -- always through the MCP write tools,
  never by editing anything directly.
- **Skills** (`plugin/user/skills/`): `/audience-score-system:research`
  (Loop 1, C4-C5), `/audience-score-system:schedule` (Loop 2, C6-C7),
  and `/audience-score-system:outcomes` (Loop 3, C9-C10) each orchestrate
  the two agents against one Channel.

This is deliberately still "MCP plus an external MCP-capable client,"
per the product brief's non-goal -- the plugin is a client-side
convenience for driving that client, not a hosted agent loop inside the
product. Schedule *commit/un-commit/edit* (C8) tools exist and are
Creator-only (`store.CanApprove`, matching `web`), but the `analyst`
persona never calls them on its own initiative -- only on the human's
explicit instruction (see `plugin/user/agents/analyst.md`).
