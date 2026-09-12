# krill — Environment Variables

M1 scaffolding (issue #2487) ships two binaries: `migrate` and `api`. Both
read the variables below. `krill/importer/cmd`'s `import` CLI (issue
#2492) reads `PG_DATABASE_URL` too (via a `--database-url` flag that
defaults to it), but is not a deployed binary and takes its other inputs
(`--path`, `--session-id`) as flags -- see `krill/README.md`'s Binaries
table. `mcp` (issue #2494, FR10/NFR1) is a third binary; see its own
section below.

## Database

Read via `//libs/go/db` (`api`) and `//libs/go/migrate` (`migrate`, which
falls back to the discrete `DB_*` vars below if `PG_DATABASE_URL` is unset
-- see `libs/go/migrate/README.md`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `PG_DATABASE_URL` | migrate, api | *(required)* | PostgreSQL connection string. `migrate` applies `krill/migrate/schema/migrations` (starting with `001_scope`) and then seeds the one `scope` row (LB1, NFR2) -- see `migrate/seed`'s package doc comment. `api` uses this to construct its connection pool (`libs/go/db.NewPool`), which `/healthz` pings on every request. |
| `DB_HOST` | migrate | `localhost` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_PORT` | migrate | `5432` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_USER` | migrate | `postgres` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_PASSWORD` | migrate | `""` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_NAME` | migrate | `postgres` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_SSL_MODE` | migrate | `disable` | Used only when `PG_DATABASE_URL` is unset. |
| `MIGRATE_AUTO_DOWN` | migrate | `false` | Allow the default run to auto-migrate DOWN when the DB is ahead of this image's migrations (e.g. after a rollback), instead of failing loudly. See `libs/go/migrate/README.md` "Rollback detection" -- a standing switch, not scoped to one rollback. |
| `MIGRATE_BYPASS_VERSION` | migrate | off | Ceiling, not a target: if the DB is ahead of this image's migrations but at or below this version, leave the schema as-is (no migration runs). |

krill's forge coordinates (`repo_full_name`, `default_branch`) are **not**
environment-driven -- `migrate/seed` seeds them explicitly as constants
(NFR2: "the seeder to do this explicitly rather than leaving it to
template inference"), since M1 runs against exactly one repo/scope.

## `api` server

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `KRILL_API_ADDR` | api | `:8080` | Address `api`'s HTTP surface listens on. |

`api` exposes `/healthz` (a live database connectivity check, not a static
200 -- `krill/api/main.go`'s doc comment), `POST /sessions/init` (FR3), the
M1 entity write endpoints (FR1/FR2/FR4, issue #2490), and the
pointer-artifact create endpoint (FR20, issue #2496) -- see
`krill/README.md`'s Endpoints table. No new configuration was added for the
FR1/FR2/FR4 write endpoints; they read the same `PG_DATABASE_URL` pool as
`/healthz`. `POST /pointer-artifacts` is the one exception -- see "Forge"
below for the one extra variable it reads.

## Forge (pointer-artifact create, issue #2496, FR20)

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `KRILL_GITHUB_TOKEN` | api | `""` | Bearer token `//krill/forge.GitHubClient` sends when creating a Product's thin pointer issue (`POST /pointer-artifacts`). Needs "Issues: write" on the scope's `repo_full_name` (a fine-grained PAT or GitHub App installation token is enough — no GitHub App credential set like `tools/app_registry`'s `GitHubDispatcherConfig` is needed for this one write). Unset leaves every other endpoint unaffected; only `POST /pointer-artifacts` fails (a GitHub 401) if it is missing or invalid. |

## `mcp` server (FR10/NFR1, issue #2494)

`mcp` exposes the FR5-FR9 scoped-slice query over MCP at `/mcp/spec`,
behind the two-front-door auth pattern already shipped in
`audience_score_system/mcp` and `whagent_net/mcp` -- see
`ARCHITECTURE.md` "The MCP spec surface" for the full design. It shares
`PG_DATABASE_URL` with `api` (same pool, both the `//krill/slice` query
layer and the mcpauth credential store read from it).

| Variable | Default | Description |
|----------|---------|--------------|
| `KRILL_MCP_ADDR` | `:8080` | Address `mcp`'s HTTP surface listens on. |
| `KRILL_MCP_PUBLIC_URL` | — | This instance's own externally reachable URL. Passed as the RFC 9728 protected-resource `resource` value and as the audience every whagent Claim this instance verifies must carry. Leaving it unset skips serving RFC 9728 metadata (`server.ResourceMetadataConfig.enabled`). |
| `KRILL_MCP_OAUTH_ISSUER` | — | The mcpauth (human) front door's OAuth2 authorization server issuer identifier, advertised in RFC 9728 metadata's `authorization_servers`. |
| `KRILL_MCP_WHAGENT_JWKS_URL` | — | whagent-net's own JWKS endpoint. Both this and `KRILL_MCP_WHAGENT_ISSUER` must be set to enable the agent front door (`server.WhagentAuthConfig`) -- left unset, `mcp` mounts only the mcpauth door, mirroring `audience_score_system/mcp`'s own pre-FR12(a) fallback. |
| `KRILL_MCP_WHAGENT_ISSUER` | — | whagent-net's own issuer identifier, verified against every whagent Claim `mcp` accepts. |

The mcpauth (human OAuth2) front door additionally requires its
`mcp_credential`-shaped table to exist against the same `PG_DATABASE_URL`
pool (`libs/go/mcpauth.NewCredentialStore`'s preflight, mirroring
`audience_score_system`'s own migration 006 and `whagent_net`'s migration
004). No such migration exists in krill yet -- until one lands, `mcp`
degrades that door to reject every call (`main.go`'s
`rejectingCredentialStore`), rather than failing to boot; the agent front
door never depends on it.

## Postgres MCP (Claude Code plugin)

`.mcp.json` at the plugin root (`krill/plugin/data/.mcp.json`, symlinked to
`.agents/plugins/krill-data` — see `.claude-plugin/marketplace.json`)
wires up three read-restricted (`--access-mode=restricted`) crystaldba
`postgres-mcp` servers via `uvx`, one per environment, following
`tools/app_registry` / `audience_score_system/plugin/data`'s identical
plugin pattern:

| Server | Connection |
|---|---|
| `krill-pg-tilt` | Hardcoded to the local default (`postgres://postgres:password@localhost:5432/krill`) — not a secret |
| `krill-pg-dev` | `KRILL_DEV_DATABASE_URI` (shell env var, not set by default) |
| `krill-pg-prod` | `KRILL_PROD_DATABASE_URI` (shell env var, not set by default) |

These are separate from `PG_DATABASE_URL` above (which `migrate`/`api`/
`mcp` read) so that tilt, dev, and prod can be queried side by side from
the same Claude Code session without swapping a single variable. This is
also separate from the `krill` plugin (`krill/plugin/user`), which gives
streamable-HTTP MCP access to `mcp`'s own FR5-FR9 slice query tools rather
than direct Postgres access.

## Telemetry

Read via `//libs/go/logging` (`api`, `mcp`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api, mcp | — | OTLP collector endpoint for traces/logs. Unset leaves telemetry export inert rather than failing boot, same convention every other domain's binaries follow. |
