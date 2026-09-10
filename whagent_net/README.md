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
Deferred to M2/Later per the roadmap: transcript archival (C18, now a
Temporal-scheduled workflow inside `worker` rather than a separate
binary — see `worker/archive.go`), `ui`/`embed` (C13–C16), `StreamEvents`
(C17), and whagent-side `allowed_tools` enforcement (C22). A
service-account caller (C10) can now start, send
turns to, and stop a session exactly as a human operator can (FR6/#2243) —
see "Client credentials (service accounts)" below. The product brief
(`PRODUCT.md`) is produced by `/project-manager:product`; milestones are
then specced with `/project-manager:design --milestone M<n>`. Origin
discussion: GitHub issue #1552.

## Binaries

| Binary | app_type | Responsibility | `bazel run` |
|--------|----------|-----------------|-------------|
| `migrate/` | `job` | Applies `session` store migrations, then seeds `agent_definition` from `config/agents.yaml` (see "Agent definition config" below). | `bazel run //whagent_net/migrate:migrate` |
| `api/` | `external-api` | Session service gRPC: start/send-turn/stop/get/list/read-transcript; publishes the JWKS every domain-owned MCP server verifies a `worker`-minted persona credential against. | `bazel run //whagent_net/api:api` |
| `worker/` | `worker` | Temporal `SessionWorkflow` + activities: resolve agent definition, build context, list/attach tools (FR8), call the model, dispatch each requested tool call, commit the turn, enforce turn/cost caps. Also hosts `ArchiveWorkflow` (FR7/C18, issue #2244, `worker/archive.go`): a Temporal Schedule periodically batches a terminal session's transcript out of Postgres past `WHAGENT_TRANSCRIPT_TTL`, gzips and uploads it to S3, commits the `transcript_archive` index row, and only then trims the hot-tier rows — registered only when `WHAGENT_S3_BUCKET` is set; there is no separate archiver binary. | `bazel run //whagent_net/worker:worker` |
| `mcp/` | `external-api` | MCP surface over `api` — how Claude Code and other agents drive agents. | `bazel run //whagent_net/mcp:mcp` |
| `ui/` | `external-api` | Standalone agent web UI (M2, issue #2236): Keycloak sign-in (NFR1) guards every app route, forwards the signed-in operator's own access token to `api` on every call (never a shared service account). A signed-in operator can start a session, watch its live transcript, send follow-up turns, and stop it (FR1/FR2, issues #2242/#2246) — a second way to drive a session alongside Claude Code/`mcp` and raw gRPC, going through the exact same `api` SessionService either way. Turn/stop controls are ownership-gated: only the session's `on_behalf_of` subject sees or can use them (LB2/NFR3). FR4's usage panel is the remaining piece. | `bazel run //whagent_net/ui:ui` |

Shared Go packages: `session/` (store), `config/` (the agent-definition
seed source), `llm/` (the OpenRouter model client), `worker/tools/` (tool
dispatch to domain-owned MCP servers), and `//libs/go/whagent` (the tool
contract those servers implement).

## Connecting Claude Code to `mcp`

The easiest path is the bundled plugin (`whagent_net/plugin/user`, listed
in the repo's marketplace as `whagent-net`, symlinked at
`.agents/plugins/whagent-net`): `claude plugin install whagent-net` (or
enable it from `/plugin`) wires up three MCP servers —
`whagent-net-mcp-tilt` (local Tilt, `localhost:8082`),
`whagent-net-mcp-dev`, and `whagent-net-mcp-prod`.

`whagent-net-mcp-tilt` forwards a fixed `Authorization: Bearer dev-local`
placeholder, per `mcp/server/auth.go`'s `PassthroughVerifier`, which
rejects any call with no bearer token even locally (Tilt's
`GRPC_AUTH_MODE=none` only skips *verifying* the token at `api`, hence the
placeholder). `whagent-net-mcp-dev` and `whagent-net-mcp-prod` ship with no
`Authorization` header at all — both environments have FR9's OAuth2 path
(below) configured, so your MCP client discovers it automatically and
walks you through a browser sign-in the first time you connect; no token
to copy or env var to set. The dev/prod hostnames follow the same
`[dev-]mcp.<domain-slug>.whalenet.<dev|app>` convention as
`audience_score_system`'s plugin.

If you'd rather use a manually-obtained bearer token against dev/prod
instead (e.g. scripting/CI, or a client that doesn't speak MCP OAuth2),
add an `Authorization: Bearer <token>` header back to that server's entry
in your own `.mcp.json` — the manual-token recipe below still works
unconditionally on every environment; the plugin just no longer assumes
you want it by default.

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

For a service-account/CI caller (no human, no browser), mint that token
with `whagent_net/scripts/kc-token.sh` (client_credentials grant against a
confidential Keycloak client with "Service accounts roles" enabled --
`libs/go/grpcauth/KEYCLOAK.md` step 4), e.g.
`export WHAGENT_DEV_ACCESS_TOKEN="$(WHAGENT_KC_TOKEN_URL=... WHAGENT_KC_CLIENT_ID=... WHAGENT_KC_CLIENT_SECRET=... whagent_net/scripts/kc-token.sh)"`.

`start_session`'s optional `first_turn` field sends that turn as soon as
the session has started (two RPC calls under the hood --
`ARCHITECTURE.md` "`mcp`'s start_session: two RPCs, one tool"). `send_turn`
returns as soon as `api` has accepted and queued a turn, never once it has
completed (FR1) -- follow up with `read_transcript` or `get_session` to see
the result.

### Browser-based sign-in (FR9)

The `Authorization: Bearer <your Keycloak access token>` recipe above --
copy a token out of band and paste it into your MCP client config -- still
works and remains supported; nothing about it changed. FR9 (issue #2245)
adds a second, interactive option for any MCP client that speaks standard
OAuth2 authorization-code + PKCE (per the MCP spec's own auth
requirements): `ui` hosts the authorization server, `mcp` is the protected
resource, and Claude Code (or any other conformant client) walks the flow
itself the first time you connect, no manual token copy required.

Point your MCP client at `mcp`'s streamable-HTTP endpoint with no
`Authorization` header at all -- an unauthenticated request there returns a
`WWW-Authenticate` challenge naming `mcp`'s own protected-resource metadata
(RFC 9728), which points at `ui` (`WHAGENT_UI_PUBLIC_URL`) as the
authorization server. From there the client performs RFC 8414 discovery
against `ui`, dynamically registers itself (RFC 7591, `POST /register`),
and opens `ui`'s `/authorize` in a browser: if you're already signed in to
`ui` it mints a credential immediately, otherwise it sends you to `/login`
first (your normal Keycloak sign-in) and resumes `/authorize` once that
completes. `POST /token` then exchanges the resulting code for the bearer
credential the client uses on every subsequent `mcp` call -- functionally
the same kind of token as the manual recipe's, just minted through a
browser flow instead of a `curl`.

The credential this mints resolves to your Keycloak `(iss, sub)` pair --
the same identity `StartSession`'s `subject` already carries for a human
operator (NFR7) -- never a separate whagent-net-only account. There is no
credential expiry/refresh/rotation or revocation UI yet (out of scope for
M2); a minted credential is valid until explicitly revoked via `mcpauth`'s
store API.

Behind the scenes (issue #2249), `mcp` never forwards that credential to
`api` as-is -- `api` verifies real Keycloak-signed JWTs only. Every call
made with this credential is transparently exchanged for a short-lived,
real Keycloak-signed JWT asserting your resolved `(iss, sub)` (RFC 8693,
cached in memory per identity until shortly before it expires), so a
session you start this way is indistinguishable downstream from one
started with a manually-pasted token. See `ARCHITECTURE.md` "`mcp`'s
OAuth2 credential and RFC 8693 token exchange" for the full design,
including the secret-custody and rotation story for the confidential
Keycloak client this uses.

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

**A service-account caller (FR6/#2243) needs the same role, granted the
same way, just to a different Keycloak principal:** the client's own
**service account user**, not a human group. `grpcauth` reads
`realm_access.roles` regardless of who the token was issued to
(`libs/go/grpcauth/KEYCLOAK.md` § "Service accounts"), so `StartSession`'s
`hasRole` check is identical for a human operator and a service account —
grant `whagent-audience-score-system-research` on the caller client's
**Service accounts roles** tab (KEYCLOAK.md § 4c) instead of via a group,
and everything else in this section applies unchanged.

## Client credentials (service accounts)

A Keycloak client-credentials caller (a scheduler, another service — no
human present) drives `SessionService` exactly as a human operator does:
`StartSession` writes `subject`/`on_behalf_of` with `kind = service`
(`on_behalf_of = subject`, since a service account acting on its own
credential acts for itself), the same `required_role` check gates it (see
above), and only that same service account — or whoever matches its
`on_behalf_of` — may later `SendTurn`/`StopSession` on the resulting
session (`canControl`, `ARCHITECTURE.md` "Identity and auth chaining").

Obtain a token the non-interactive way (`libs/go/grpcauth/KEYCLOAK.md` §
7's `curl` recipe, `grant_type=client_credentials`) and call `api` or `mcp`
with it exactly as the cross-domain smoke check below does with a bearer
token — there is no separate service-account API surface. Note that FR9's
OAuth2 browser flow (`ui`/`mcp`'s interactive sign-in, once built) does
**not** apply here: a service account has no browser to redirect, and never
authenticates through it — `grant_type=client_credentials` is the only
grant a service-account caller ever uses.

```bash
TOKEN=$(curl -s -X POST "$KEYCLOAK_TOKEN_URL" \
  -d grant_type=client_credentials \
  -d client_id="$WHAGENT_TEST_CLIENT_ID" \
  -d client_secret="$WHAGENT_TEST_CLIENT_SECRET" | jq -r .access_token)

grpcurl -plaintext -H "authorization: Bearer $TOKEN" \
  -d '{"agent_id": "audience-score-system-research"}' \
  localhost:50054 whagent.v1.SessionService/StartSession
```

This requires a real Keycloak realm (`GRPC_AUTH_MODE=oidc`) — the checked-in
Tiltfile runs `api` with `GRPC_AUTH_MODE=none`, which always injects a fixed
human-looking dev caller (`grpcauth.Claims.IsServiceAccount` defaults
`false`) and so cannot locally exercise this path end to end (see
`libs/go/grpcauth/KEYCLOAK.md` § "Service accounts"). M2 proves this path
against a **test** service-account client (`WHAGENT_TEST_CLIENT_ID`/
`WHAGENT_TEST_CLIENT_SECRET` above are placeholders for whatever that test
credential is named once a shared dev Keycloak realm exists) — no real
scheduler integrates with whagent-net yet.

## Local development

Requires Postgres (`PG_DATABASE_URL`, also backs `ui`'s own DB-backed
session store), Temporal (`TEMPORAL_HOST`), RabbitMQ (`RABBITMQ_URL`,
`worker` only today), an OpenRouter API key (`OPENROUTER_API_KEY`), and a
whagent-net signing key (`WHAGENT_SIGNING_KEY`/`WHAGENT_SIGNING_KEY_ID`,
`api` and `worker` both fail startup loudly without one) — see
[`ENV.md`](ENV.md) for the complete variable set across every binary.

**Tilt** (`cd whagent_net && tilt up`) stands up `migrate`/`api`/`worker`/
`mcp`/`ui` plus Postgres/Temporal/RabbitMQ, with a checked-in dev-only
signing key — see `Tiltfile`. `worker`'s transcript archive schedule
stays inert by default (no local S3-compatible storage in this Tiltfile
yet; set `WHAGENT_S3_BUCKET`/`S3_ENDPOINT`/`S3_ACCESS_KEY`/`S3_SECRET_KEY`
in a local `.env`, pointed at a MinIO of your own, to exercise it).
`ui` defaults to `AUTH_MODE=none` locally (no Keycloak
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
bazel run //whagent_net/ui:ui                 # the standalone agent web UI
```
