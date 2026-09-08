# whagent-net — Environment Variables

> Skeleton. No binary reads any of these yet (pre-M1). Rows are the variable
> families the architecture commits to; fill in defaults and per-component
> ownership as each binary lands, per `AGENTS.md` § Maintaining Docs.

## Database

Read via `//libs/go/db` (`api`, `worker`, `archiver`, `ui`) and
`//libs/go/migrate` (`migrate`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `PG_DATABASE_URL` | all | *(required)* | PostgreSQL connection string for the `session` store. |

## Temporal

Read via `//libs/go/temporal`'s `ConfigFromEnv` (`api` to start/signal
workflows, `worker` to host them).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `TEMPORAL_HOST` | api, worker | — | Temporal frontend address. |
| `TEMPORAL_NAMESPACE` | api, worker | — | Namespace. |
| `TEMPORAL_TASK_QUEUE` | api, worker | — | Task queue for `SessionWorkflow`. |

## RabbitMQ (event bus)

Read via `//libs/go/rmq` (`worker` publishes; `archiver`, `ui`, and any
`embed` host consume via `//libs/go/htmxsse`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `RABBITMQ_URL` | worker, archiver, ui | — | Broker URL (`amqp://` or `amqps://`). Exchange name `whagent/events` is fixed. Unset disables publishing: `whagent_net/session`'s transcript append path still commits, it just skips the publish step (see `whagent_net/events`, issue #2111). |
| `RABBITMQ_SSL_VERIFY` | worker, archiver, ui | `true` | For `amqps://` URLs only: set to `false` to skip server certificate verification (dev/test only). Read by `//libs/go/rmq`. |
| `RABBITMQ_CA_CERT_PATH` | worker, archiver, ui | — | For `amqps://` URLs only: path to a custom CA certificate file. Read by `//libs/go/rmq`. |
| `RABBITMQ_TLS_SERVER_NAME` | worker, archiver, ui | — | For `amqps://` URLs only: server name for certificate verification, for when the connection URL's host differs from the certificate's. Read by `//libs/go/rmq`. |

## S3 (cold tier)

Read by `archiver` (write) and `api` (hydrate archived transcripts).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_S3_BUCKET` | archiver, api | — | Bucket for `sessions/{id}.jsonl`. |
| `WHAGENT_TRANSCRIPT_TTL` | archiver | — | Hot-tier retention after a session is terminal. |

## LLM provider

Read by `worker`.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OPENROUTER_API_KEY` | worker | *(required)* | OpenRouter API key. |
| `OPENROUTER_BASE_URL` | worker | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL; swapping it is how a second provider would be introduced. |
| `WHAGENT_DEFAULT_MODEL` | worker, api | — | Model used when neither agent definition nor session specifies one. |
| `WHAGENT_DEFAULT_MAX_TURNS` | api | `100` | Per-session turn cap default. |
| `WHAGENT_DEFAULT_MAX_COST_USD` | api | `1` | Per-session cost cap default. |

## Identity (OIDC / Keycloak)

Read by `api` (token verification + authorization), `ui` and `mcp`
(sign-in / token acquisition).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_OIDC_ISSUER` | api, ui, mcp | — | Keycloak realm issuer URL. |
| `WHAGENT_OIDC_CLIENT_ID` | ui, mcp | — | OIDC client for the interactive surfaces. |
| `WHAGENT_OIDC_CLIENT_SECRET` | ui, mcp | — | Client secret. |
| `WHAGENT_OIDC_AUDIENCE` | api | — | Expected audience on tokens presented to `api`. |

## Service wiring

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_API_URL` | mcp, ui, embed hosts | — | `api` gRPC address. |
