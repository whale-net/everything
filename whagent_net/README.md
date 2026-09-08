# whagent-net

WHale AGENT NETwork — the Temporal-backed AI agent framework for this repo.
Sessions run as long-lived Temporal workflows, transcripts live in a
hot/cold tiered store, and every UI is a stateless reader — so an agent
session survives worker restarts, can be watched from several UIs at once,
and can be embedded into any other domain's web UI as a Go component.

Language/runtime is **Go throughout**, reusing `//libs/go/temporal`,
`//libs/go/db`, `//libs/go/migrate`, `//libs/go/rmq`, `//libs/go/htmxsse`,
and the `htmx*` UI libraries. See [`ARCHITECTURE.md`](ARCHITECTURE.md) for
the component map and design decisions.

## Status

**M1 in progress.** The `session` store schema and `migrate` job exist
(issue #2109): `whagent_net/migrate` applies `001_initial_schema` (see
`whagent_net/migrate/migrations/`), and `whagent_net/session` exposes the
store interfaces (`SessionStore`, `TranscriptStore`, `AgentDefinitionStore`,
`UsageStore`, `IdempotencyLedger`) other M1 tasks build on. No other binary
below exists yet. The product brief (`PRODUCT.md`) is produced by
`/project-manager:product`; milestones are then specced with
`/project-manager:design --milestone M<n>`. Origin discussion: GitHub issue
#1552.

## Planned binaries

| Binary | app_type | Responsibility |
|--------|----------|----------------|
| `migrate/` | `job` | Applies `session` store migrations. |
| `api/` | `external-api` | Session service gRPC: start/send-turn/stop/get/list/read-transcript. |
| `worker/` | `worker` | Temporal `SessionWorkflow` + activities (context build, LLM call, tool dispatch, commit). |
| `archiver/` | `worker` | Postgres → S3 transcript archival and hot-tier retention. |
| `mcp/` | `external-api` | MCP surface over `api` — how Claude Code and other agents drive agents. |
| `ui/` | `external-api` | Standalone agent UI (session list, session view, run an agent). |

Shared Go packages: `session/` (store), `embed/` (embeddable session UI
component), and `//libs/go/whagent` (tool contract for domain-owned MCP
servers).

## Connecting Claude Code to `mcp`

`mcp` (issue #2120) is a thin MCP facade over `api`'s `SessionService`:
`start_session`, `send_turn`, `stop_session`, `get_session`, and
`read_transcript` -- identical to what's available over gRPC (FR1/FR2/FR3),
and nothing more (no agent discovery, no wait-for-completion tool -- see
`ARCHITECTURE.md` "Open items"). It runs it as an unauthenticated
`GET /healthz` plus a streamable-HTTP MCP endpoint at `/` that requires a
bearer token -- your own Keycloak access token, forwarded byte-for-byte to
`api` as the caller (never a shared service account, per FR10/`ARCHITECTURE.md`
"Identity and auth chaining").

Add it to Claude Code's MCP config (`claude mcp add` or your
`.mcp.json`) as a streamable-HTTP server pointed at `mcp`'s listen address
(`WHAGENT_MCP_ADDR`, see `ENV.md`), e.g.:

```json
{
  "mcpServers": {
    "whagent-net": {
      "type": "http",
      "url": "http://localhost:8082/",
      "headers": {
        "Authorization": "Bearer <your Keycloak access token>"
      }
    }
  }
}
```

`start_session`'s optional `first_turn` field sends that turn as soon as
the session has started (two RPC calls under the hood --
`ARCHITECTURE.md` "`mcp`'s start_session: two RPCs, one tool"). `send_turn`
returns as soon as `api` has accepted and queued a turn, never once it has
completed (FR1) -- follow up with `read_transcript` or `get_session` to see
the result.

## Local development

Will require Postgres (`PG_DATABASE_URL`), Temporal (`TEMPORAL_HOST`),
RabbitMQ (`RABBITMQ_URL`), and an S3-compatible bucket for the archiver.
Concrete `bazel run` targets and Tilt wiring land with M1; see
[`ENV.md`](ENV.md) for the variable set as it is defined.
