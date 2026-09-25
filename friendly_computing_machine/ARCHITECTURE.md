# Friendly Computing Machine — Architecture

> System design for the Slack bot, its Temporal workflows, and the OIDC identity-link web app.
> Read this before adding new commands, workflows, or integrations.

## Overview

Friendly Computing Machine is a Slack bot (Socket Mode) that turns `@mention`s into whagent-net AI
sessions and runs ad-hoc polls. Slack-facing commands are quick; anything long-running is handed to a
Temporal workflow so it survives restarts and can be queried later.

The bot also exposes one small inbound HTTP surface — the **identity-link web app** — used only to bind
a Slack user to their Keycloak identity so whagent-net's `on_behalf_of` delegation can act as them.
An `@mention` in an agent-linked channel is gated on that binding: a user with no stored mapping is
prompted with a one-time link (see below) instead of being given a session.

## Components

| Component | Entry point | Role |
|---|---|---|
| Bot (Socket Mode) | `bot run-slack-socket-app` | Slack event handling; queues work, posts replies. |
| Task pool | `bot run-taskpool` | Background execution for bot tasks. |
| Workflow worker | `workflow run` | Temporal worker that runs the long-running workflows and calls whagent-net. |
| Identity-link web | `web run` | FastAPI app that performs the browser OIDC login and writes the Slack→Keycloak mapping. |
| Migration job | `migration run` | Applies Alembic migrations. |

### Identity-link web app (`web`)

`friendly_computing_machine/src/friendly_computing_machine/web/` is a small FastAPI app. It performs a
standard OIDC authorization-code login against the Keycloak realm whagent-net uses, via Authlib's
`authlib.integrations.starlette_client.OAuth`. It serves only static redirect/result pages — no HTMX,
no rich UI.

Two tables back the flow (see `db/dal/identity_dal.py`):

- **`slacklinktoken`** — a one-time, short-lived (10 min) link token minted for a Slack user. It carries
  a link attempt across the Keycloak redirect so the callback can identify *which* Slack identity to
  bind without trusting anything from the browser.
- **`slackkeycloakidentity`** — the durable one-row-per-Slack-user mapping from
  `(slack_team_id, slack_user_id)` to `(keycloak_iss, keycloak_sub)`.

#### Link flow

1. `GET /link/{token}` — validates the token with `peek_link_token`. An unknown, expired, or already
   consumed token renders a static error page (no redirect). A valid token is stashed in the Starlette
   session (alongside Authlib's own OIDC `state`/`nonce`) and the browser is redirected to Keycloak's
   authorize endpoint.
2. `GET /link/callback` — completes the code exchange with `authorize_access_token`. Authlib verifies
   the ID token (no custom verification). The verified `iss`/`sub` claims are read and
   `complete_link(token, iss, sub)` is called, which consumes the token and upserts the mapping in a
   single transaction. The session is cleared afterwards.

Guarantees:
- The DB row is the sole authority for the Slack binding — the session only carries a reference to it.
- On any failure or abandonment mid-flow, nothing is written: consumption and the mapping write happen
  in one transaction, so a failed or replayed link leaves the DB unchanged.
- No access, refresh, or ID token is ever persisted or logged; only the `(iss, sub)` pair is stored.
- A successful link logs at INFO with the Slack team/user ids and `iss`/`sub` (never tokens).

#### Link gating (bot)

`bot/handlers/whagent.py`'s `handle_whagent_app_mention` is **block-until-linked**. After the channel's
agent link resolves and *before* any workflow is started, it looks up `get_keycloak_identity(team_id,
user_id)`. If the mentioning user has no stored mapping it:

1. mints a one-time token with `mint_link_token(team_id, user_id)`,
2. posts a Slack `chat_postEphemeral` (visible only to that user) containing a clickable
   `${FCM_WEB_PUBLIC_URL}/link/<token>` link, and
3. returns without starting anything.

Because the check runs before `start_workflow` (and before any `slackthreadsession` row is written), a
blocked mention never leaves an orphaned `ACTIVE` thread-session row. The prompt is self-contained — no
slash command or out-of-band instruction. A prompt issuance logs at INFO with the Slack team/user ids
(never the token). If `FCM_WEB_PUBLIC_URL` is unset the mention is still blocked and an ERROR is logged.
Multi-participant attribution inside an already-linked thread is out of scope.

## Integrations

- **whagent-net** — `@mention`-triggered AI sessions. The bot queues a turn, the workflow worker calls
  whagent-net's `api` over gRPC with a service-account Keycloak token, and the reply is posted back to
  the Slack thread. See [docs/whagent_integration.md](docs/whagent_integration.md).
- **ManMan** — none. The manman V1 integration (server-control actions, the
  server-select shortcut, the `/test` debug command, and the RabbitMQ
  `subscribe` relay) was removed; see [docs/deploy_recovery.md](docs/deploy_recovery.md)
  for what the removal did to the database.
- **Keycloak** — whagent-net service accounts (`WHAGENT_*`) and the web app's confidential browser-login
  client (`FCM_OIDC_*`) both authenticate against the same realm. See [ENV.md](ENV.md).
- **ArgoCD** — deploy notifications. See [docs/argocd-integration.md](docs/argocd-integration.md).

## Slack special channel route tables

`slackspecialchanneltype` and `slackchannel` rows in `slackspecialchannel` are a
generic two-level route table — "channels of type T" — that FCM will use to route
Slack messages by channel. Nothing populates them from this repository and
nothing reads them yet; they are retained deliberately while that work is
scoped, and the models, the migrations, and the two lookups
(`get_slack_special_channel_type_from_name`, `get_slack_special_channels_from_type`)
are kept alive in `db/dal/slack_dal.py` for it.

**A lookup that returns nothing is a legitimate state, not a defect.** Retaining
the tables does not mean they are populated: the one channel type that ever
existed was inserted out of band, by hand, as an operator-managed production
artifact, and there is no seeder here to re-create it. `SlackSpecialChannelTypeEnum`
is deliberately an empty enum — its vocabulary is the routing work's to choose,
and an empty enum is not a bug. Do not add a data migration to populate it.
