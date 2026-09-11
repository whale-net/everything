# whagent-net — Environment Variables

M1 shipped (issue #2121): `migrate`, `api`, `worker`, and `mcp` all read
the variables below. `ui` (M2, issue #2236) now exists too — see "`ui`
(standalone agent web UI, issue #2236)" below for its own variables.
Hot-to-cold transcript archival (M2, FR7/C18, issue #2244) is not a
separate binary — it runs as a Temporal-scheduled workflow inside
`worker` (`worker/archive.go`) — see "S3 (cold tier)" and "Archive
schedule (issue #2244)" below for its variables, both read by `worker`.

## Database

Read via `//libs/go/db` (`api`, `worker`, `ui`) and
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

Read via `//libs/go/rmq` (`worker` publishes; `api` (`StreamEvents`, FR5/C17,
issue #2239), `ui`, and any `embed` host consume -- `api` via a
raw `rmq.Consumer` (`whagent_net/api/main.go`'s `initializeEventsConsumer`),
the others via `//libs/go/htmxsse`). `worker`'s archive schedule (issue
#2244) is not a consumer here -- it selects archivable sessions by
periodically polling `sessions`/`transcript_archive` directly (see
"Archive schedule" below), not by watching the bus.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `RABBITMQ_URL` | worker, api, ui | — | Broker URL (`amqp://` or `amqps://`). Exchange name `whagent/events` is fixed. Unset disables publishing on `worker` (`whagent_net/session`'s transcript append path still commits, it just skips the publish step -- see `whagent_net/events`, issue #2111) and disables `StreamEvents` on `api` (the RPC reports `UNAVAILABLE` instead of blocking; `api` itself still starts, same non-fatal-construction convention as `worker`'s publisher). |
| `RABBITMQ_SSL_VERIFY` | worker, api, ui | `true` | For `amqps://` URLs only: set to `false` to skip server certificate verification (dev/test only). Read by `//libs/go/rmq`. |
| `RABBITMQ_CA_CERT_PATH` | worker, api, ui | — | For `amqps://` URLs only: path to a custom CA certificate file. Read by `//libs/go/rmq`. |
| `RABBITMQ_TLS_SERVER_NAME` | worker, api, ui | — | For `amqps://` URLs only: server name for certificate verification, for when the connection URL's host differs from the certificate's. Read by `//libs/go/rmq`. |

## S3 (cold tier)

Read by `worker` (write, via its `ArchiveWorkflow`/`RunArchiveBatch`,
FR7, issue #2244, `worker/archive.go`) and `api` (hydrate archived
transcripts, FR8, issue #2240). `api` builds its `//libs/go/s3` client in
`initializeS3Client` (`whagent_net/api/main.go`) and attaches it to the
`session.Store` via `session.WithS3` -- construction is non-fatal, same
pattern as `RABBITMQ_URL`/`initializePublisher` above: `WHAGENT_S3_BUCKET`
unset, or the client failing to construct, leaves transcript reads
hot-only (`ReadTranscript` still works for every non-archived session; see
`whagent_net/session/transcript.go`'s `TranscriptStore` doc comment).
`worker` builds its own `//libs/go/s3` client the same non-fatal way
(`initializeS3Client`, `whagent_net/worker/main.go`): `WHAGENT_S3_BUCKET`
unset, or the client failing to construct, simply means `worker` never
registers `ArchiveWorkflow` or its Temporal Schedule at all -- there is no
separate archiver binary to fail startup loudly the way one used to.
`S3_REGION`/`S3_ENDPOINT`/`S3_ACCESS_KEY`/`S3_SECRET_KEY` are the same
unprefixed names `manmanv2/api` and `tools/app_registry` use for their own
`s3.Client`s (see `libs/go/s3` `Config`) -- only the bucket is
whagent-net-specific.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_S3_BUCKET` | worker, api | — | Bucket for `sessions/{id}.jsonl.gz`. Unset disables the cold tier for `api` (reads stay hot-only) and disables `worker`'s archive schedule entirely (no fatal startup error either way). |
| `S3_REGION` | worker, api | `us-east-1` | Region for the S3-compatible endpoint. |
| `S3_ENDPOINT` | worker, api | — | Custom S3 endpoint (e.g. MinIO, OVH); unset uses AWS's default endpoint resolution. |
| `S3_ACCESS_KEY` | worker, api | — | Static access key (e.g. for MinIO); unset falls back to the AWS SDK's default credential chain. |
| `S3_SECRET_KEY` | worker, api | — | Static secret key, paired with `S3_ACCESS_KEY`. |
| `WHAGENT_TRANSCRIPT_TTL` | worker | `168h` (7 days) | Hot-tier retention after a session is terminal (`sessions.updated_at`, the compare-and-swap terminal write) before it becomes eligible for archival. |

## Archive schedule (issue #2244)

Read directly via `os.Getenv` in `whagent_net/worker/main.go`
(`ArchiveConfigFromEnv`/`ArchiveInterval`, `whagent_net/worker/archive.go`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_ARCHIVE_INTERVAL` | worker | `5m` | How often the Temporal Schedule (`ArchiveScheduleID`) fires `ArchiveWorkflow`, which scans for newly-eligible sessions (terminal, past `WHAGENT_TRANSCRIPT_TTL`, no `transcript_archive` row yet). |
| `WHAGENT_ARCHIVE_BATCH_SIZE` | worker | `50` | Maximum number of eligible sessions archived per scheduled run, so one run never tries an unbounded backlog in one pass. |

## LLM provider

Read by `worker` (`//whagent_net/llm`, issue #2112) and, for `OPENROUTER_API_KEY`/
`OPENROUTER_BASE_URL`/`WHAGENT_MODEL_CATALOG_TTL` only, by `api` as well
(issue #2117): `StartSession` builds its own `llm.Client`/`llm.Catalog` pair
to check a requested `model_override` against the provider catalogue (FR5)
before any session row is written -- a separate in-process cache from
`worker`'s, since the two are different binaries sharing no memory.

OpenRouter's own upstream-provider routing (restricting/ranking/excluding
which of OpenRouter's inference vendors may serve a call -- `only`,
`ignore`, `order`, `quantizations`, `sort`, `allow_fallbacks`,
`require_parameters`, `data_collection`, per
[OpenRouter's provider routing](https://openrouter.ai/docs/features/provider-routing))
is not an env var: it is configured per agent definition via the
`model_definition` table (`whagent_net/session/modeldef.go`,
`config/agents.yaml`'s `model_definitions`), not here -- see
`ARCHITECTURE.md`'s "Component map" for `model_definition`. This is
unrelated to `OPENROUTER_BASE_URL` below -- OpenRouter remains the only
LLM provider whagent-net talks to; `model_definition` only narrows which
of *its* upstream inference vendors may serve a call, and does so
per-agent rather than process-wide.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OPENROUTER_API_KEY` | worker, api | *(required)* | OpenRouter API key. |
| `OPENROUTER_BASE_URL` | worker, api | `https://openrouter.ai/api/v1` | OpenAI-compatible base URL; swapping it is how a second provider would be introduced. |
| `WHAGENT_DEFAULT_MODEL` | worker, api | — | Model used when neither agent definition nor session specifies one. |
| `WHAGENT_DEFAULT_MAX_TURNS` | api | `100` | Per-session turn cap default. |
| `WHAGENT_DEFAULT_MAX_COST_USD` | api | `1` | Per-session cost cap default. |
| `WHAGENT_MODEL_CATALOG_TTL` | worker, api | `5m` | How long `llm.Catalog` caches OpenRouter's model list (FR5) before refetching. |
| `WHAGENT_PRICE_TABLE_PATH` | worker | *(required)* | Path to the per-model price table `llm.LoadPriceTable` reads (LB6: contents and source stay cheap to change -- a config file, not a code table). JSON object keyed on model id, e.g. `{"openai/gpt-4o": {"prompt_usd_per_million": 2.5, "completion_usd_per_million": 10}}`; read fresh on every call, so an edit takes effect without a code change. |

`WHAGENT_DEFAULT_MODEL` and `WHAGENT_PRICE_TABLE_PATH` are both still unread by any binary today -- every seeded agent in `config/agents.yaml` names its own model, and `start.go`'s only fallback is the per-session `model_override` (FR5). Once the default-model fallback and a checked-in price table are actually implemented, the intended values are: `WHAGENT_DEFAULT_MODEL=z-ai/glm-5.3-flash`, priced in the table at `{"z-ai/glm-5.3-flash": {"prompt_usd_per_million": 0.075, "completion_usd_per_million": 0.25}}`.

## Identity (OIDC / Keycloak)

Read by `api` (token verification + authorization), `ui` and `mcp`
(sign-in / token acquisition).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_OIDC_ISSUER` | api, ui, mcp | — | Keycloak realm issuer URL. Also stamped as the on-behalf-of `SubjectIssuer` `worker` mints into every session's persona Claim (issue #2150) — it only needs to be non-empty; `whagent.Sign` carries no verification requirement on its value, only on its presence (`libs/go/whagent/sign.go`). `whagent_net/Tiltfile` defaults this to a fixed `dev-issuer` placeholder locally, overridable via a local `.env`. |
| `WHAGENT_OIDC_CLIENT_ID` | ui, mcp | — | OIDC client for the interactive surfaces. |
| `WHAGENT_OIDC_CLIENT_SECRET` | ui, mcp | — | Client secret. |
| `WHAGENT_OIDC_REDIRECT_URI` | ui | `http://localhost:8080/auth/callback` | `ui`'s OIDC callback URL, registered with `WHAGENT_OIDC_CLIENT_ID` as a valid redirect URI in Keycloak. Mounted at `/auth/callback` regardless of this value's path (see "`ui`" below) — this only needs to match what Keycloak is configured to redirect back to. |
| `WHAGENT_OIDC_AUDIENCE` | api | — | Expected audience on tokens presented to `api`. |

## Delegated grant (issue #2426, plan #2421)

Read by `ui` and `mcp` (`//whagent_net/delegatedgrant.Build`, called from
each binary's own `main.go`) to construct the single shared confidential
Keycloak client, `libs/go/grpcauth/pgstore`-backed `Store`
(`grpcauth_delegated_grant` table, `whagent_net/migrate/schema/
migrations/008_delegated_grant`), and `libs/go/grpcauth/grantindex`-backed
bookkeeping index (`grpcauth_grant_index`, same migration) that plan
#2421 migrates `whagent_net` onto. **Purely additive as of issue #2426:**
`ui`'s `/authorize` still mints an opaque `mcpauth` credential and `mcp`
still exchanges via `WHAGENT_MCP_KEYCLOAK_*` above — nothing reads or
writes either new table yet. A dependent task swaps both request paths
onto this wiring (FR8/FR9).

**NFR5: one shared client, not one per domain.** Every variable below
configures a *single* confidential Keycloak client used as the caller
identity by both `ui` and `mcp` — unlike `WHAGENT_MCP_KEYCLOAK_*` above
(mcp's own, separate client) or a per-consuming-domain client
(`KEYCLOAK.md`'s usual "one client per caller identity" principle, § 11):
domain isolation for this flow is carried entirely by the grant key
derived from `AgentDefinition.Domain` (`//whagent_net/grantkey`, FR4), not
by provisioning a separate Keycloak client per domain. Both binaries must
be configured with the *same* `WHAGENT_GRANT_CLIENT_ID`/
`WHAGENT_GRANT_CLIENT_SECRET`/`WHAGENT_GRANT_REDIRECT_URI`/
`WHAGENT_GRANT_ENCRYPTION_KEY` values.

Every variable below is either unset on both binaries together (the
`whagent_net/Tiltfile` local-dev default — construction degrades to a
`WARNING` log and a nil `Components`, mirroring `WHAGENT_MCP_KEYCLOAK_*`'s
own degrade precedent above) or set on both together — a *partial*
configuration (e.g. every variable but `WHAGENT_GRANT_CLIENT_SECRET`) is a
fatal startup error naming the missing variable, on both binaries: see
`//whagent_net/delegatedgrant`'s `Build` doc comment for why a partial
configuration is never allowed to silently construct a client that would
only fail at its first real token call.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_GRANT_CLIENT_ID` | ui, mcp | — | The one shared confidential client's id in Keycloak (same realm as `WHAGENT_OIDC_ISSUER` above). Distinct from `WHAGENT_OIDC_CLIENT_ID` (which only ever verifies or forwards a token) and from `WHAGENT_MCP_KEYCLOAK_CLIENT_ID` (`mcp`'s own RFC 8693 token-exchange client, being retired by this same plan). See `KEYCLOAK.md` § 11 for the Keycloak-side client setup this requires (confidential, `offline_access` scope, refresh-token rotation enabled). |
| `WHAGENT_GRANT_CLIENT_SECRET` | ui, mcp | — | Secret for `WHAGENT_GRANT_CLIENT_ID`. Never checked in, never logged, never echoed in an error (NFR5) — provisioned as a Kubernetes secret, identically on both binaries. |
| `WHAGENT_GRANT_REDIRECT_URI` | ui, mcp | — | The browser-consent redirect URI, allow-listed on `WHAGENT_GRANT_CLIENT_ID` in Keycloak (NFR5's redirect-URI-as-security-control, `KEYCLOAK.md` § 11). Must be `ui`'s own `GET /mcp/consent/callback` route (issue #2428's `handleMCPConsentCallback`, `WHAGENT_UI_PUBLIC_URL` + `/mcp/consent/callback`) — the only place a browser is ever redirected back to after `BeginAuthorization`. `mcp` reads the same value solely because `grpcauth.DelegatedGrantConfig.RedirectURI` is a required field regardless of whether a given holder ever drives the interactive leg. |
| `WHAGENT_GRANT_ENCRYPTION_KEY` | ui, mcp | — | Secret, SHA-256-hashed into `pgstore`'s required 32-byte AES-256-GCM key encrypting `grpcauth_delegated_grant.token_material` at rest — mirrors `audience_score_system`'s `ASS_TOKEN_ENCRYPTION_KEY` derivation (`audience_score_system/ENV.md`). Must be identical on both binaries — a mismatch means whichever binary didn't mint a grant's ciphertext cannot decrypt it. Never checked in, never logged, never echoed in an error. |

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
| `GRPC_AUTH_MODE` | api | `none` | `none` or `oidc` (`//libs/go/grpcauth.AuthMode`). `none` injects dev claims for every call and logs a startup warning -- development only; every RPC still requires *some* claims (`handlers.RequireClaimsUnaryInterceptor`), so `none` is "skip token verification," never "skip authentication." In `none` mode the injected dev claims' roles are every seeded agent's `required_role` (`config.RequiredRoles`, sourced from `whagent_net/config/agents.yaml`), not a fixed list -- so FR9's `required_role` check passes for any seeded agent under the checked-in local Tilt config (issue #2154). |

## `mcp` server (issue #2120)

Read directly via `os.Getenv` in `whagent_net/mcp/main.go`. `mcp` is a
pure facade over `api`'s `SessionService` -- `WHAGENT_MCP_ADDR`/
`WHAGENT_API_URL` are the only two addresses the manual-token recipe
needs (plus the Identity variables above, which its
`PassthroughVerifier`/`AuthMiddleware` use to reject a call before any
tool handler runs, never to verify the token itself -- `api` remains the
sole verification boundary per FR10). The rest of this table (FR9, issue
#2249) is additive and entirely optional: unset, `mcp` behaves exactly as
it did before that issue -- no RFC 9728 discovery endpoint, no
`mcp_credential` probe, no OAuth2 token-exchange path -- and the
manual-token recipe above keeps working end to end regardless (see
`whagent_net/mcp/server/transport.go`'s `NewHTTPHandler` and
`whagent_net/mcp/main.go`'s `initializeTokenExchange`, both non-fatal on
a missing value, mirroring `ui`'s `initializeSSEHub` convention).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_MCP_ADDR` | mcp | `:8082` | Listen address for `mcp`'s streamable-HTTP MCP surface (`GET /healthz` unauthenticated, `/` requiring a bearer token). |
| `WHAGENT_API_URL` | mcp | *(required)* | `api`'s gRPC address -- the only outbound dependency this binary dials (see "Service wiring" above). |
| `WHAGENT_MCP_PUBLIC_URL` | mcp | — | This binary's own externally reachable base URL -- must be byte-identical to `ui`'s own `WHAGENT_MCP_PUBLIC_URL` (above, "`ui`" section) -- the RFC 9728 `resource` this binary advertises at `/.well-known/oauth-protected-resource`. Unset skips serving that endpoint entirely; a mismatch with `ui`'s value silently breaks an MCP client's discovery instead. |
| `WHAGENT_UI_PUBLIC_URL` | mcp | — | `ui`'s own externally reachable base URL (matches `ui`'s own `WHAGENT_UI_PUBLIC_URL`) -- the OAuth2 authorization server issuer this binary's RFC 9728 metadata names. Unset alongside `WHAGENT_MCP_PUBLIC_URL` above also skips serving that endpoint. |
| `PG_DATABASE_URL` | mcp | — | Backs a `mcpauth.CredentialStore` against the same `mcp_credential` table `ui`'s OAuth2 provider mints into (the "Database" section above; `whagent_net/migrate/schema/migrations/004_mcpauth_credential`, issue #2245). Unset, unreachable, or a missing table all degrade to "OAuth2 credential path unavailable" (logged at `WARNING`), never a failed boot -- **except** the one NFR8 combination below: reachable + table present but `WHAGENT_MCP_KEYCLOAK_CLIENT_ID`/`_CLIENT_SECRET`/`_TOKEN_URL` not fully set, which fails the boot loudly instead (`main.go`'s `initializeTokenExchange`), since that combination would otherwise make the OAuth2 path reachable while every exchange on it fails opaquely at request time. `whagent_net/Tiltfile` leaves this and the three `WHAGENT_MCP_KEYCLOAK_*` vars below unset by default for exactly this reason (that NFR8 combination is otherwise the Tiltfile's default) -- set `ENABLE_WHAGENT_NET_MCP_OAUTH=true` plus the three vars in a local `.env` to opt in. |
| `WHAGENT_MCP_KEYCLOAK_CLIENT_ID` | mcp | — | `mcp`'s own confidential Keycloak client id for the RFC 8693 token exchange (NFR8) -- distinct from `WHAGENT_OIDC_CLIENT_ID` above, which only ever verifies or forwards a token, never mints one. See `ARCHITECTURE.md` § "Identity and auth chaining" ("`mcp`'s OAuth2 credential and RFC 8693 token exchange") for the full secret-custody writeup (what holding this secret lets a process do, and the rotation procedure). |
| `WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET` | mcp | — | Secret for `WHAGENT_MCP_KEYCLOAK_CLIENT_ID`. Never checked in, never logged, never echoed in an error (NFR8) -- provisioned as a Kubernetes secret. |
| `WHAGENT_MCP_KEYCLOAK_TOKEN_URL` | mcp | — | Keycloak's token endpoint URL for the realm `WHAGENT_OIDC_ISSUER` names, e.g. `https://keycloak.example.com/realms/whagent/protocol/openid-connect/token` -- where the RFC 8693 `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` request is sent. |

`PG_DATABASE_URL` above also gates issue #2426's purely-additive
delegated-grant wiring (`main.go`'s `initializeDelegatedGrant`, reusing
the same pool the FR9 credential store above opened) -- see "Delegated
grant (issue #2426, plan #2421)" above for the four `WHAGENT_GRANT_*`
variables it also needs.

## `ui` (standalone agent web UI, issue #2236)

Read directly via `os.Getenv` in `whagent_net/ui/main.go`. `ui` requires
Keycloak sign-in for every app route (NFR1: no whagent-net-specific
login mechanism, no local user table) via `//libs/go/htmxauth`, and
forwards the signed-in operator's own access token to `api` on every
call (`//libs/go/grpcauth`) rather than a shared service account --
mirrors `mcp`'s FR10 stance. `WHAGENT_OIDC_ISSUER`/`WHAGENT_OIDC_CLIENT_ID`/
`WHAGENT_OIDC_CLIENT_SECRET`/`WHAGENT_OIDC_REDIRECT_URI` above are its
Keycloak sign-in configuration; `WHAGENT_API_URL` above is `api`'s gRPC
address, the only outbound dependency this binary dials; `PG_DATABASE_URL`
(the "Database" section above) backs `ui`'s own DB-backed session store
(`ui_sessions` table, via `htmxauth.NewDBSessionManager`) -- a distinct
Postgres *table* from `whagent_net/session`'s domain tables even though it
shares the same connection string, since `ui` never queries the domain
tables directly, only through `api`'s gRPC surface. `ui` also hosts
mcpauth's OAuth2 authorization-server front end (FR9/C27, issue #2245) --
`WHAGENT_UI_PUBLIC_URL`/`WHAGENT_MCP_PUBLIC_URL` below configure it; it
shares `PG_DATABASE_URL` too (`mcp_credential`/`mcp_oauth_client`/
`mcp_auth_code` tables, `whagent_net/migrate/schema/migrations/
004_mcpauth_credential`). `PG_DATABASE_URL` also backs issue #2426's
purely-additive delegated-grant wiring (`main.go`'s
`initializeDelegatedGrant`) -- see "Delegated grant (issue #2426, plan
#2421)" above for the four `WHAGENT_GRANT_*` variables it needs.

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `WHAGENT_UI_ADDR` | ui | `:8080` | Listen address for `ui`'s HTTP surface (`GET /healthz` unauthenticated, every other route requiring a Keycloak session). |
| `AUTH_MODE` | ui | `none` | `none` (dev-only synthetic `dev-user`, `//libs/go/htmxauth.AuthModeNone`) or `oidc` (real Keycloak sign-in, NFR1). Matches manmanv2/ui's and app-registry-ui's own literal `AUTH_MODE` name. |
| `GRPC_AUTH_MODE` | ui | `none` | `none` or `oidc` (`//libs/go/grpcauth.AuthMode`) -- gates whether the operator's access token is actually forwarded to `api` on outbound calls. Should match `api`'s own `GRPC_AUTH_MODE` above. |
| `WHAGENT_UI_PUBLIC_URL` | ui | *(required)* | `ui`'s own externally-reachable base URL, e.g. `https://whagent.example.com` -- FR9/issue #2245's `mcpauth.ProviderConfig.Issuer`, the base every mcpauth endpoint URL `ui` advertises (`/authorize`, `/token`, `/register`, `/.well-known/oauth-authorization-server`) is built from. Mirrors `audience_score_system`'s `ASS_OAUTH_REDIRECT_BASE_URL` doubling as mcpauth's issuer (see `audience_score_system/ENV.md`). |
| `WHAGENT_MCP_PUBLIC_URL` | ui | *(required)* | `mcp`'s own externally-reachable base URL -- FR9's `mcpauth.ProviderConfig.Resource`, the OAuth2 `resource` identifier both binaries must agree on exactly. Must be byte-identical to what `mcp` itself advertises in its own protected-resource metadata (a dependent task, issue #2245's Context section) -- a mismatch breaks an MCP client's RFC 9728 discovery chain. |
| `SECRET_KEY` | ui | `dev-secret-key-change-in-production` | Encrypts `ui`'s DB-backed session store's access/refresh tokens, and (issue #2428) the short-lived, httpOnly cookie `handlers_consent.go`'s `pendingConsent` round-trips through between `BeginAuthorization` and its Keycloak-redirect callback. Matches manmanv2/ui's and app-registry-ui's own literal `SECRET_KEY` name; distinct from `WHAGENT_SIGNING_KEY` above (JWKS signing, a different purpose entirely). |
| `WHAGENT_UI_DEFAULT_DOMAIN` | ui | — | The one `AgentDefinition.Domain` (issue #2424's FR1) `authorizeConsentGate` (`handlers_consent.go`, issue #2428) requires an active delegated grant for before `/authorize` mints an MCP-client credential — see that file's package doc comment for why this is a single configured domain rather than a live multi-domain chooser (`ui` has no `agent_definition`-listing API to build one from; that table stays behind `api`'s gRPC surface per `ARCHITECTURE.md`). Unset disables the `/authorize` gate entirely (the standalone `GET /mcp/consent?domain=<d>` route, issue #2428, is unaffected either way). |
