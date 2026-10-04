# Integrating with whagent-net

How a domain MCP server (manmanv2, ASS, …) and a delegating client (fcm's
Slack bot, …) plug into whagent-net, and which identity each check sees.
Read this before wiring any new MCP server or client.

## The authorization model: two layers

Every agent session has two identities: the **caller** (whoever called
`StartSession`) and the **on-behalf-of user** (who the session acts for).
They are the same person unless a delegating client sets `on_behalf_of`.

| Layer | Question | Identity checked | Enforced by |
|---|---|---|---|
| **1. Launch** | May this caller start this agent? | The **caller's** Keycloak token (`realm_access.roles`) | whagent-net `StartSession`, against the agent's `required_role` |
| **2. Action** | May this user do this thing? | The **on-behalf-of user** | The domain's MCP server, on every `tools/list` and `tools/call` |

- **Layer 1 never sees the on-behalf-of user.** On a delegated start
  whagent-net receives only the user's `(iss, sub)`, never a token for
  them, so it has no user roles to check. A delegating client therefore
  needs each agent's `required_role` on **its own service account**. That
  makes `required_role` the per-client allowlist of launchable agents: a
  client with no roles can launch no gated agents.
- **Layer 2 never sees the caller.** whagent-net mints a short-lived Claim
  per tool server whose `sub`/`sub_iss` are the on-behalf-of user
  (`libs/go/whagent`). The domain maps that pair to its own user and
  authorizes exactly as it would for that user directly. Per-user
  permissions belong here, not in `required_role`.

This is ordinary delegated authorization: the client is authorized for the
capability, the resource server authorizes the user.

## Adding a domain MCP server

1. **Verify whagent Claims.** Accept JWTs whose `iss` equals whagent-net's
   `WHAGENT_ISSUER`, verified against whagent-net `api`'s JWKS
   (`http://<whagent-net-api>:8090/.well-known/jwks.json`), with `aud`
   equal to this server's public URL.
2. **Map `(sub_iss, sub)` to a domain user** and authorize every tool
   call as that user. Never authorize on the Claim's `act` (the agent).
3. **Provide an identity link** so the mapping exists: whagent-net `ui`'s
   `/grants` page sends a `WHAGENT_UI_SIGNING_KEY`-signed assertion to the
   domain's `/link/whagent?token=...`, and the domain stores
   `(iss, sub) → user`. An unlinked caller must get a tool error naming
   the "Link <domain> identity" action, never an empty result.
4. **Use one Keycloak issuer string everywhere.** whagent-net's
   `WHAGENT_OIDC_ISSUER`, every delegating client's issuer, and the
   domain's own OIDC issuer must be byte-identical; `iss`/`sub` lookups
   are exact string matches.
5. **Add the agent definition** (`config/agents.yaml`, then a new
   `agent_definition` row — see `README.md` "Agent definition config"):
   `tool_set.server_url` must equal the `aud` from step 1 verbatim,
   trailing slash included.
6. **Create the agent's realm role** (`whagent-<agent_id>`) and grant it to
   the human group that may launch it from whagent-net directly, and to
   each delegating client that should offer it (below).

## Adding a delegating client

1. **Give the client a Keycloak service account** (client-credentials
   grant) in the same realm.
2. **Allowlist it for delegation:** add its `client_id` to whagent-net
   `api`'s `WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` (`ENV.md`).
3. **Grant agent roles to its service account** (Keycloak → Clients →
   the client → Service accounts roles), one per agent it should launch.
   Grant none by default.
4. **Always send `on_behalf_of`** with the end user's linked Keycloak
   `(iss, sub)`. If the user has no link, prompt them to link and start
   nothing; never fall back to a session that runs as the client itself.

## Worked examples

| | ASS | manmanv2 |
|---|---|---|
| Agent | `audience-score-system-research` | `manmanv2-ops` |
| `required_role` | `whagent-audience-score-system-research` | `whagent-manmanv2-ops` |
| Claim verification env | `ASS_WHAGENT_JWKS_URL` / `ASS_WHAGENT_ISSUER` | `MCP_WHAGENT_JWKS_URL` / `MCP_WHAGENT_ISSUER` |
| `(iss, sub)` → user | Auto-provisions a `Person` on first sight; "Link ASS identity" ties it to an existing one | `whagent_identity_link` row from "Link manmanv2 identity"; required |
| Per-user authorization | Channel roles (`channel_person`) | Persona from the user's own realm roles; calls run with the user's own token |
| Details | `audience_score_system/architecture/02-mcp-server.md` | `manmanv2/ENV.md` (`MCP_WHAGENT_*`) |

fcm is the delegating client: it calls with its own service account and
sends the Slack user's linked identity as `on_behalf_of`
(`friendly_computing_machine/docs/whagent_integration.md`).

## Troubleshooting

| Symptom | Layer | Fix |
|---|---|---|
| `PermissionDenied: caller lacks required role "<role>"` | 1 | Grant the role to the caller — the client's service account on a delegated start, not the end user |
| `PermissionDenied: caller is not permitted to start a session on behalf of another subject` | 1 | Add the client's `client_id` to `WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` |
| Tool error `unauthenticated: whagent identity could not be resolved` | 2 | The user opens whagent-net `/grants` and runs "Link <domain> identity" |
| Domain rejects the token as invalid | 2 | `aud` ≠ `server_url`, or `iss`/`sub_iss` differs from the domain's configured issuers |
