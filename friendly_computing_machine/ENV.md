# Friendly Computing Machine — Environment Variables

> Every environment variable the FCM code reads, with its purpose, whether it is
> required, and which app reads it. Read this when configuring, deploying, or
> debugging. This file is intended to be sufficient on its own — you should not
> need to read the source to find a variable.

See [docs/slack_tokens.md](docs/slack_tokens.md) for Slack token setup details.

## Release apps

| App | Chart app name | Command |
|---|---|---|
| Slack bot | `bot` | `fcm bot --log-otlp run-slack-socket-app --skip-migration-check` |
| Task pool | `taskpool` | `fcm bot --log-otlp run-taskpool --skip-migration-check` |
| Temporal worker | `worker` | `fcm workflow --log-otlp run --skip-migration-check` |
| Identity-link web | `web` | `fcm web --log-otlp run --skip-migration-check` |
| Migration job | `migration` | `fcm migration --log-otlp run` (a Helm `pre-install,pre-upgrade` hook) |

`fcm` is the single multi-command binary; the release app name and the CLI
sub-command are different things. `bot`, `taskpool`, `worker`, and `web` are
long-running; `migration` is a Job that exits when the migrations are done.

## Core variables

Read by every app. These are the ones to set first.

| Variable | Required | Read by | Purpose |
|---|---|---|---|
| `POSTGRES_URL` | yes | all apps | SQLAlchemy/psycopg2 database URL, e.g. `postgresql+psycopg2://user:pass@host:5432/fcm`. FCM's tables live in the `fcm` schema; Alembic's version table lives in `public`. |
| `SLACK_BOT_TOKEN` | yes (not `web`, not `migration`) | `bot`, `taskpool`, `worker` | Slack bot OAuth token (`xoxb-…`) used for every Web API call and for the Socket Mode connection. |
| `SLACK_APP_TOKEN` | yes for Socket Mode | `bot` | Slack app-level token (`xapp-…`), used only to open the Socket Mode connection. The task pool and worker post over the Web API and do not need it, so the CLI treats it as optional. |
| `TEMPORAL_HOST` | yes (not `web`, not `migration`) | `bot`, `taskpool`, `worker` | Temporal frontend address, `host:port`, e.g. `localhost:7233`. |
| `GOOGLE_API_KEY` | yes (not `web`, not `migration`) | `bot`, `taskpool`, `worker` | Google AI Studio key backing the `/wai` and `@mention` answer generation. |
| `APP_ENV` | optional | all apps | Deployment environment name; defaults to `dev`. Part of the Temporal namespace/queue naming, so a mismatch between apps puts them on different queues. Set it to the same value everywhere. |

`GEMINI_MODEL` (read by `gemini/client.py`, default `gemini-2.5-flash`) overrides
the model used for answer generation. Set it only if you are deliberately
pinning a different model.

## whagent-net integration

See [docs/whagent_integration.md](docs/whagent_integration.md) for the feature these configure.

| Variable | Required | Purpose |
|---|---|---|
| `WHAGENT_API_URL` | yes | whagent-net `api`'s gRPC address (`host:port`), e.g. `localhost:50054`. |
| `WHAGENT_UI_PUBLIC_URL` | yes | whagent-net `ui`'s externally-reachable base URL — used to build the `{url}/sessions/{id}` link posted in the first thread reply. |
| `WHAGENT_KEYCLOAK_TOKEN_URL` | yes | Keycloak token endpoint fcm's service account uses to obtain a `client_credentials` grant. |
| `WHAGENT_CLIENT_ID` | yes | fcm's whagent-net service-account Keycloak client id. |
| `WHAGENT_CLIENT_SECRET` | yes | fcm's whagent-net service-account Keycloak client secret. |

These are required on both `bot run-slack-socket-app` and `bot run-taskpool`
(the shared `fcm bot` callback reads them before either sub-command runs) and on
`workflow run`. They are **not** required by `web` or `migration`.

whagent-net's own `api` server has two settings that decide whether the
delegated path is reachable at all — `GRPC_AUTH_MODE` and
`WHAGENT_ON_BEHALF_OF_ALLOWED_CLIENT_IDS` — which fcm does not read and which
are documented in [whagent_net/ENV.md](../whagent_net/ENV.md), not here. The
allowlist ships empty, which fails closed; see
[docs/oidc_link_e2e_verification.md](docs/oidc_link_e2e_verification.md).

## OIDC identity link web app (`web run`)

The `web` app performs the browser OIDC login that links a Slack user to their
Keycloak identity. It runs against the same Keycloak realm as whagent-net's
`api`/`ui`.

| Variable | Required | Purpose |
|---|---|---|
| `FCM_WEB_PUBLIC_URL` | yes | Externally-reachable base URL of this app (e.g. `https://fcm-web.example.com`). Used to build the OIDC callback URL `${FCM_WEB_PUBLIC_URL}/link/callback`, and — on the `bot` app — the one-time `${FCM_WEB_PUBLIC_URL}/link/<token>` link posted as an ephemeral prompt when an unlinked user `@mention`s an agent-linked channel. Required on both `web run` and `bot run-slack-socket-app`. |
| `FCM_OIDC_ISSUER_URL` | yes | Keycloak realm issuer URL (e.g. `https://keycloak.example.com/realms/whagent`). Authlib fetches `<issuer>/.well-known/openid-configuration`. |
| `FCM_OIDC_CLIENT_ID` | yes | Keycloak client id for the **confidential browser-login** client (distinct from the `WHAGENT_CLIENT_ID` service account). |
| `FCM_OIDC_CLIENT_SECRET` | yes | Keycloak client secret for the confidential browser-login client. |
| `FCM_WEB_SESSION_SECRET` | yes | Signing key for the Starlette session cookie that carries Authlib's OIDC `state`/`nonce` and the one-time link token across the redirect. |
| `FCM_WEB_PORT` | no | Port the app listens on (default `8000`). |

Required Keycloak client config (provisioned through normal release/human steps, not by this repo):
- Type: **Confidential**, standard flow enabled.
- Valid redirect URI: exactly `${FCM_WEB_PUBLIC_URL}/link/callback`.

All of these are required on `web run`. The browser-facing client id/secret are
intentionally separate from the `WHAGENT_*` service-account credentials.

If `FCM_WEB_PUBLIC_URL` is unset on the `bot` app, an `@mention` from an
unlinked user is still blocked and an ERROR is logged — the link is simply
never posted.

## RabbitMQ connection

These are the generic `RABBITMQ_*` variables defined by the shared
`libs/python/cli/providers/rabbitmq` provider, which every Python service in
this repo uses. **FCM's own CLIs do not read them today** — the only consumer
that did was the manman V1 subscribe service, which M1 removed. They are
documented here because they remain part of the shared provider surface, are
still read by other domains against the same broker, and a future FCM consumer
would pick them up with no code change to this document.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `RABBITMQ_HOST` | yes | `localhost` | Broker hostname. |
| `RABBITMQ_PORT` | yes | `5672` | Broker port (`5671` for TLS). |
| `RABBITMQ_USER` | no | `guest` | Broker username. |
| `RABBITMQ_PASSWORD` | no | `guest` | Broker password. |
| `RABBITMQ_VHOST` | no | `/` | Virtual host. |
| `RABBITMQ_ENABLE_SSL` | no | `false` | Connect over TLS. |
| `RABBITMQ_SSL_HOSTNAME` | no | unset | SNI/verification hostname used when SSL is enabled. Leave unset only for a trusted-network plaintext broker. |

The `external_service_events` exchange name is likewise shared across domains.
FCM does not read it from the shared registry — it duplicated the string
literal in the code it has now removed — so do not change the name on the
broker; manman V1's subscribers in other domains still depend on it.

## Logging and telemetry

Read by every app's CLI callback (`libs/python/cli/providers/logging`). No FCM
log line depends on these being set; they are all optional.

| Variable | Default | Purpose |
|---|---|---|
| `LOG_LEVEL` | `INFO` | `DEBUG`/`INFO`/`WARNING`/`ERROR`/`CRITICAL`. An unrecognized value falls back to `INFO`. |
| `LOG_OTLP` | `false` | `true`/`1`/`yes` enables OTLP log export. The release apps pass `--log-otlp` on the command line, which overrides this. |
| `LOG_CONSOLE` | `true` | `true`/`1`/`yes` enables the console OTLP exporter. |
| `LOG_JSON_FORMAT` | `false` | `true`/`1`/`yes` emits JSON lines instead of plain text. |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | `http://0.0.0.0:4317` | OTLP logs endpoint, used when log export is enabled. `OTEL_EXPORTER_OTLP_ENDPOINT` is the fallback. |

These resource-attribute variables are read by `libs/python/logging` to label
telemetry. The Helm chart sets `APP_NAME`, `APP_DOMAIN`, `APP_TYPE`,
`APP_VERSION`, and friends automatically; you only need them when running
outside the chart.

| Variable | Purpose |
|---|---|
| `APP_NAME`, `APP_DOMAIN`, `APP_TYPE`, `APP_VERSION` | Service/resource identity attached to exported telemetry. |
| `APP_ENV` or `ENVIRONMENT` | Deployment environment label. `APP_ENV` is FCM's own variable (see above). |
| `GIT_COMMIT` or `COMMIT_SHA` | Source revision label. |
| `POD_NAME`, `CONTAINER_NAME`, `NODE_NAME`, `NAMESPACE` or `POD_NAMESPACE`, `HOSTNAME`, `PLATFORM`, `ARCHITECTURE`, `BAZEL_TARGET` | Kubernetes/build resource attributes. |

## Container timezone

`TZ` is **not** an FCM variable and nothing here reads it, but it is the one
environment change that can silently break the weekly music poll: the poll
window is compared across a timezone-naive and a timezone-aware column, so the
comparison is only correct while the app container's timezone matches the
database session's. Nothing in the chart sets `TZ` today, so do not set it
without reading [docs/poll.md](docs/poll.md) first.
