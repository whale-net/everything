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
