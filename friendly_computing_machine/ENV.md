# Friendly Computing Machine — Environment Variables

> All environment variables required to run the Slack bot.
> Read this when configuring, deploying, or debugging.

See [docs/slack_tokens.md](docs/slack_tokens.md) for Slack token setup details.

## Variables

<!-- TODO: Document each env var — SLACK_BOT_TOKEN, TEMPORAL_ADDRESS, etc. — with purpose, required/optional, and default -->

### whagent-net integration

See [docs/whagent_integration.md](docs/whagent_integration.md) for the feature these configure.

| Variable | Purpose |
|---|---|
| `WHAGENT_API_URL` | whagent-net `api`'s gRPC address (`host:port`), e.g. `localhost:50054`. |
| `WHAGENT_UI_PUBLIC_URL` | whagent-net `ui`'s externally-reachable base URL — used to build the `{url}/sessions/{id}` link posted in the first thread reply. |
| `WHAGENT_KEYCLOAK_TOKEN_URL` | Keycloak token endpoint fcm's service account uses to obtain a `client_credentials` grant. |
| `WHAGENT_CLIENT_ID` | fcm's whagent-net service-account Keycloak client id. |
| `WHAGENT_CLIENT_SECRET` | fcm's whagent-net service-account Keycloak client secret. |

Required on both `bot run-slack-socket-app` (posts to Slack) and `workflow run` (runs the Temporal worker that actually calls whagent-net).

### OIDC identity link web app (`web run`)

The `web` app performs the browser OIDC login that links a Slack user to their Keycloak identity. It runs against the same Keycloak realm as whagent-net's `api`/`ui`.

| Variable | Purpose |
|---|---|
| `FCM_WEB_PUBLIC_URL` | Externally-reachable base URL of this app (e.g. `https://fcm-web.example.com`). Used to build the OIDC callback URL `${FCM_WEB_PUBLIC_URL}/link/callback`, and — on the `bot` app — the one-time `${FCM_WEB_PUBLIC_URL}/link/<token>` link posted as an ephemeral prompt when an unlinked user `@mention`s an agent-linked channel. Required on both `web run` and `bot run-slack-socket-app`. |
| `FCM_OIDC_ISSUER_URL` | Keycloak realm issuer URL (e.g. `https://keycloak.example.com/realms/whagent`). Authlib fetches `<issuer>/.well-known/openid-configuration`. |
| `FCM_OIDC_CLIENT_ID` | Keycloak client id for the **confidential browser-login** client (distinct from the `WHAGENT_CLIENT_ID` service account). |
| `FCM_OIDC_CLIENT_SECRET` | Keycloak client secret for the confidential browser-login client. |
| `FCM_WEB_SESSION_SECRET` | Signing key for the Starlette session cookie that carries Authlib's OIDC `state`/`nonce` and the one-time link token across the redirect. |
| `FCM_WEB_PORT` | Optional. Port the app listens on (default `8000`). |

Required Keycloak client config (provisioned through normal release/human steps, not by this repo):
- Type: **Confidential**, standard flow enabled.
- Valid redirect URI: exactly `${FCM_WEB_PUBLIC_URL}/link/callback`.

All of these are required on `web run`. The browser-facing client id/secret are intentionally separate from the `WHAGENT_*` service-account credentials.
