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

Read via `//libs/go/temporal`'s `ConfigFromEnv` (`worker`, #2114, to host
`SessionWorkflow`; `api`, #2117, to dial the same Temporal frontend and
start/signal it -- `StartSession`/`SendTurn`/`StopSession`, never a worker
itself).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `TEMPORAL_HOST` | api, worker | `localhost:7233` | Temporal frontend address. |
| `TEMPORAL_NAMESPACE` | api, worker | `default` | Namespace. |
| `TEMPORAL_TASK_QUEUE` | api, worker | `whagent-net-session` | Task queue `SessionWorkflow` and its activities run on. Unset on either side falls back to the same `"whagent-net-session"` default (`worker/workflow.go`'s `TaskQueue` const, duplicated in `api/handlers/session.go` as `sessionWorkflowTaskQueue` since a `worker` is `package main` and cannot be imported) -- only set this explicitly if running more than one `SessionWorkflow` task queue, and set it identically on both `api` and `worker`. |

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

Read by `worker` (`//whagent_net/llm`, issue #2112) and, for `OPENROUTER_API_KEY`/
`OPENROUTER_BASE_URL`/`WHAGENT_MODEL_CATALOG_TTL` only, by `api` as well
(issue #2117): `StartSession` builds its own `llm.Client`/`llm.Catalog` pair
to check a requested `model_override` against the provider catalogue (FR5)
before any session row is written -- a separate in-process cache from
`worker`'s, since the two are different binaries sharing no memory.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OPENROUTER_API_KEY` | worker, api | *(required)* | OpenRouter API key. |
| `OPENROUTER_BASE_URL` | worker, api | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL; swapping it is how a second provider would be introduced. |
| `WHAGENT_DEFAULT_MODEL` | worker, api | — | Model used when neither agent definition nor session specifies one. |
| `WHAGENT_DEFAULT_MAX_TURNS` | api | `100` | Per-session turn cap default. |
| `WHAGENT_DEFAULT_MAX_COST_USD` | api | `1` | Per-session cost cap default. |
| `WHAGENT_MODEL_CATALOG_TTL` | worker, api | `5m` | How long `llm.Catalog` caches OpenRouter's model list (FR5) before refetching. |
| `WHAGENT_PRICE_TABLE_PATH` | worker | *(required)* | Path to the per-model price table `llm.LoadPriceTable` reads (LB6: contents and source stay cheap to change -- a config file, not a code table). JSON object keyed on model id, e.g. `{"openai/gpt-4o": {"prompt_usd_per_million": 2.5, "completion_usd_per_million": 10}}`; read fresh on every call, so an edit takes effect without a code change. |

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

## Persona claim issuance (trust root, issue #2115)

`api` owns whagent-net's signing key(s) and publishes the public JWKS
(`whagent_net/api/persona`, read directly via `os.Getenv` like the `api`
server variables below). `worker` (#2118) reads the *same* signing-key
variables — `WHAGENT_ISSUER`/`WHAGENT_SIGNING_KEY`/`WHAGENT_SIGNING_KEY_ID`
— to construct its own `persona.Issuer` and mint each tool call's claim
in-process (see `ARCHITECTURE.md` "Identity and auth chaining" §
"Issuance mechanism"); there is no RPC between the two. `api` never falls
back to an unsigned or symmetric mode — it fails startup loudly when the
active key is missing or unparseable.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_ISSUER` | api, worker | `whagent-net` | The `iss` value every minted Claim carries — whagent-net's own issuer, never a domain's or a Keycloak realm's. |
| `WHAGENT_SIGNING_KEY` | api, worker | *(required)* | PEM-encoded PKCS8 asymmetric private signing key (e.g. `openssl genpkey -algorithm ed25519`) — the active key `Issuer.Issue` mints with. Never checked in. |
| `WHAGENT_SIGNING_KEY_ID` | api, worker | *(required)* | The JWKS `kid` for `WHAGENT_SIGNING_KEY` — what a `whagent.Verifier` uses to select the matching public key on rotation. |
| `WHAGENT_SIGNING_KEYS_ADDITIONAL` | api | — | Optional JSON array of `{"kid": "...", "private_key_pem": "..."}` entries for retired keys — published in the JWKS response only (never used to mint), kept only long enough for a token signed moments before rotation to still verify until it expires. Introducing or dropping an entry is a config change, never a code redeploy. |
| `WHAGENT_JWKS_ADDR` | api | `:8090` | Listen address for the `net/http` mux serving `/.well-known/jwks.json` (`persona.JWKSPath`), alongside `api`'s gRPC surface. |

## `api` server (SessionService, issue #2113)

Read directly via `os.Getenv` in `whagent_net/api/main.go` (not
`//libs/go/db`/`ConfigFromEnv` conventions above, which cover the
database/Temporal/RabbitMQ/S3 client libraries this binary also uses).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `PORT` | api | `50051` | gRPC listen port for `SessionService`. |
| `GRPC_AUTH_MODE` | api | `none` | `none` or `oidc` (`//libs/go/grpcauth.AuthMode`). `none` injects dev claims for every call and logs a startup warning -- development only; every RPC still requires *some* claims (`handlers.RequireClaimsUnaryInterceptor`), so `none` is "skip token verification," never "skip authentication." |

## `mcp` server (issue #2120)

Read directly via `os.Getenv` in `whagent_net/mcp/main.go`. `mcp` is a
pure facade over `api`'s `SessionService` -- these are the only two
addresses it needs (plus the Identity variables above, which its
`PassthroughVerifier`/`AuthMiddleware` use to reject a call before any
tool handler runs, never to verify the token itself -- `api` remains the
sole verification boundary per FR10).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_MCP_ADDR` | mcp | `:8082` | Listen address for `mcp`'s streamable-HTTP MCP surface (`GET /healthz` unauthenticated, `/` requiring a bearer token). |
| `WHAGENT_API_URL` | mcp | *(required)* | `api`'s gRPC address -- the only outbound dependency this binary dials (see "Service wiring" above). |
