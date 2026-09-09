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

**M1 shipped.** An operator can run a capped, tool-enabled session as
themselves against `audience_score_system`'s MCP server from Claude Code,
and read its transcript — the milestone's outcome sentence, exercised end
to end (issue #2121). `migrate`, `api`, `worker`, and `mcp` all exist and
build; `whagent_net/config/agents.yaml` seeds one real agent definition
(`audience-score-system-research`) targeting `audience_score_system/mcp`.
Deferred to M2/Later per the roadmap: the `archiver` (C18), `ui`/`embed`
(C13–C16), `StreamEvents` (C17), a service-account caller path (C10), and
whagent-side `allowed_tools` enforcement (C22). The product brief
(`PRODUCT.md`) is produced by `/project-manager:product`; milestones are
then specced with `/project-manager:design --milestone M<n>`. Origin
discussion: GitHub issue #1552.

## Binaries

| Binary | app_type | Responsibility | `bazel run` |
|--------|----------|-----------------|-------------|
| `migrate/` | `job` | Applies `session` store migrations, then seeds `agent_definition` from `config/agents.yaml` (see "Agent definition config" below). | `bazel run //whagent_net/migrate:migrate` |
| `api/` | `external-api` | Session service gRPC: start/send-turn/stop/get/list/read-transcript; publishes the JWKS every domain-owned MCP server verifies a `worker`-minted persona credential against. | `bazel run //whagent_net/api:api` |
| `worker/` | `worker` | Temporal `SessionWorkflow` + activities: resolve agent definition, build context, list/attach tools (FR8), call the model, dispatch each requested tool call, commit the turn, enforce turn/cost caps. | `bazel run //whagent_net/worker:worker` |
| `mcp/` | `external-api` | MCP surface over `api` — how Claude Code and other agents drive agents. | `bazel run //whagent_net/mcp:mcp` |
| `ui/` | `external-api` | Standalone agent web UI (M2, issue #2236): Keycloak sign-in (NFR1) guards every app route, forwards the signed-in operator's own access token to `api` on every call (never a shared service account). No session-specific pages yet — a placeholder authenticated index page today; FR1-FR4 land the real pages. | `bazel run //whagent_net/ui:whagent-net-ui` |

Planned, not yet built (M2+): `archiver/` (Postgres → S3 transcript
archival and hot-tier retention).

Shared Go packages: `session/` (store), `config/` (the agent-definition
seed source), `llm/` (the OpenRouter model client), `worker/tools/` (tool
dispatch to domain-owned MCP servers), and `//libs/go/whagent` (the tool
contract those servers implement).

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

## Agent definition config

`whagent_net/config/agents.yaml` is the checked-in source of truth
`whagent_net/migrate/seed` upserts into the `agent_definition` table on
every `migrate` run — config-driven seeding, but `agent_definition` stays
a real, versioned table (LB5/NFR6), never a config-lookup shortcut: the
seeder never edits a version already pinned to a session in place, it
only inserts a new one when a config entry's fields (`model`, `tool_set`,
`max_turns`, `max_cost_usd`, `required_role`) drift from the latest
seeded version. Re-running the seeder with an unchanged config is a
no-op. Before writing anything, the seeder also checks every entry's
`model` against the configured OpenRouter provider's live catalogue
(`OPENROUTER_API_KEY`/`OPENROUTER_BASE_URL`, see `ENV.md`) — an unserved
model fails the whole `migrate` run loudly rather than writing a
half-seeded table, so `migrate` needs outbound network access to
OpenRouter even in local dev.

To add or change a seeded agent definition, edit `agents.yaml` and
re-run `migrate` (`bazel run //whagent_net/migrate:migrate`, or the Tilt
job below) — see that file's own doc comment for the exact field shape.

## Keycloak role

FR9's authorization check (`api`'s `StartSession` handler) requires the
acting subject to hold the seeded agent definition's `required_role` — a
**realm role** (`libs/go/grpcauth/KEYCLOAK.md`'s "Gotcha 1": `grpcauth`
reads `realm_access.roles` only, never a client role) checked against
`grpcauth.Claims.Roles`. The seeded `audience-score-system-research`
definition (`config/agents.yaml`) requires:

```
whagent-audience-score-system-research
```

**To create and grant it** (see `libs/go/grpcauth/KEYCLOAK.md` §§ 2, 5
for the full mental model — realm roles are global to the realm, so this
name is already prefixed `whagent-` to avoid colliding with another
domain's roles):

1. Admin console → your realm → **Realm roles** → **Create role** → name
   it exactly `whagent-audience-score-system-research` → Save.
2. Grant it to a human operator via a **group** (KEYCLOAK.md § 5: "Humans
   get roles via groups, never individually") — create or reuse a group,
   add the role to the group's **Role mapping**, add the operator to the
   group.
3. The realm role must also land in the acting subject's token's
   `realm_access.roles` claim, which it does automatically once granted
   (no separate mapper needed for realm roles, unlike the audience
   mapper client roles require — KEYCLOAK.md § 4).

**To verify it appears in `grpcauth.Claims.Roles`:** obtain a token for
an operator who holds the role (interactively, or
`grant_type=password`/`client_credentials` per KEYCLOAK.md § 7's curl
recipe) and decode it — `realm_access.roles` in the JWT payload must list
`whagent-audience-score-system-research`. `api`'s
`RequireClaimsUnaryInterceptor` (`GRPC_AUTH_MODE=oidc`) puts the decoded
token's roles on `grpcauth.Claims.Roles` for every RPC; `StartSession`'s
`hasRole` check (`api/handlers/start.go`) is what actually gates the
call — a `start_session` for this agent as an operator without the role
must fail with `PermissionDenied` and no session row created (M1
Validation criterion 1).

Realm configuration itself (creating the role, the group, and granting
it) is **manual** in this repo today — there is no in-repo Keycloak
realm-config-as-code for whagent-net, so this section is the deliverable
per the milestone's own scope (`AGENTS.md` § Documentation Conventions).

## Local development

Requires Postgres (`PG_DATABASE_URL`, also backs `ui`'s own DB-backed
session store), Temporal (`TEMPORAL_HOST`), RabbitMQ (`RABBITMQ_URL`,
`worker` only today), an OpenRouter API key (`OPENROUTER_API_KEY`), and a
whagent-net signing key (`WHAGENT_SIGNING_KEY`/`WHAGENT_SIGNING_KEY_ID`,
`api` and `worker` both fail startup loudly without one) — see
[`ENV.md`](ENV.md) for the complete variable set across all five
binaries.

**Tilt** (`cd whagent_net && tilt up`) stands up all five binaries plus
Postgres/Temporal/RabbitMQ, with a checked-in dev-only signing key —
see `Tiltfile`. `ui` defaults to `AUTH_MODE=none` locally (no Keycloak
realm required to click around), forwarded to
[http://localhost:8081](http://localhost:8081) — set `AUTH_MODE=oidc`
plus the `WHAGENT_OIDC_*` vars in a local `.env` to exercise a real
Keycloak sign-in. The seeded agent definition targets
`audience_score_system/mcp`, which has its own minimal Tiltfile
(`cd audience_score_system && tilt up`, run alongside this one) wired
with the matching whagent-net trust configuration
(`ASS_WHAGENT_JWKS_URL`/`ASS_WHAGENT_ISSUER`).

**Cross-domain smoke check** (issue #2155). After both Tiltfiles are up,
confirm the FR8 cross-domain tool-dispatch path is wired correctly end
to end without needing a full Claude Code MCP client — `api`'s
SessionService is a plain gRPC service (reflection enabled), so
`grpcurl` reaches it directly:

```bash
SESSION_ID=$(grpcurl -plaintext -d '{"agent_id": "audience-score-system-research"}' \
  localhost:50054 whagent.v1.SessionService/StartSession | jq -r '.session.sessionId')
grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\", \"input\": \"list your tools\"}" \
  localhost:50054 whagent.v1.SessionService/SendTurn
grpcurl -plaintext -d "{\"session_id\": \"$SESSION_ID\"}" \
  localhost:50054 whagent.v1.SessionService/GetSession
```

`GetSession`'s `state` should be `SESSION_STATE_RUNNING`, or (absent an
`OPENROUTER_API_KEY` in a sandboxed environment) `SESSION_STATE_FAILED`
with `errorDetail` naming the model-call step — never a failure naming
claim minting ("Mint requires a non-empty SubjectIssuer", #2150) or tool
listing ("Unauthorized", #2151).

**`bazel run`**, in order (each blocks in the foreground; use separate
terminals):

```bash
bazel run //whagent_net/migrate:migrate       # applies migrations + seeds agent_definition
bazel run //whagent_net/api:api               # SessionService gRPC + JWKS
bazel run //whagent_net/worker:worker         # SessionWorkflow
bazel run //whagent_net/mcp:mcp               # the Claude-Code-facing MCP surface
bazel run //whagent_net/ui:whagent-net-ui     # the standalone agent web UI
```
