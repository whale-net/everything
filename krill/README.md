# krill

krill is the spec-of-record and work-tracking substrate for agent swarms —
see [`PRODUCT.md`](PRODUCT.md) for the vision, personas, load-bearing
decisions, and milestone roadmap.

This task (issue #2487) stands krill up as a Bazel domain following the
`whagent_net` / `audience_score_system` template: a `migrate` job, an `api`
binary skeleton, and the `scope` table (LB1) every other table in this
milestone hangs off. No spec entities exist yet — that is later M1 work.

## Binaries

| Binary | Target | Type | Description |
|--------|--------|------|-------------|
| `migrate` | `//krill/migrate` | job | Applies `krill/migrate/schema/migrations` and seeds the one `scope` row with this repo's forge coordinates (LB1, NFR2). |
| `api` | `//krill/api` | external-api | HTTP server; `/healthz` (a live DB ping) and `POST /sessions/init` (FR3's `init` primitive, issue #2489). No spec entity write endpoints yet. |

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | Live DB connectivity check. Never gated. |
| `POST /sessions/init` | Mints a krill-native session id (FR3). Body: `{"scope_id": "<uuid>", "acting": {"iss", "sub", "kind"}, "on_behalf_of": {"iss", "sub", "kind"}, "whagent_session_id": "<optional string>"}`; `kind` is `human` or `service`. Returns `{"session_id": "<uuid>"}`. Every write endpoint added by a later M1 task (#2490/#2492/#2493/#2496) requires the resulting id on an `X-Krill-Session-Id` header (`api/handlers/gate.go`'s `RequireSession`) — see `ARCHITECTURE.md` "`init` and the write gate" for why `init` itself takes the caller's identity fields as-is rather than verifying a bearer credential. |

## Local development

```sh
# Build everything in the domain
bazel build //krill/...

# Run the migrate job directly against a local Postgres
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/migrate

# Run api
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/krill?sslmode=disable \
  bazel run //krill/api
curl http://localhost:8080/healthz
```

Or bring up the whole domain (Postgres + migrate + api) via Tilt:

```sh
cd krill && tilt up
```

See [`ENV.md`](ENV.md) for every environment variable `migrate` and `api`
read, and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the component map and
the `scope` table's design rationale.

## Claude Code plugin

`plugin/user/` is the Claude Code plugin layout this domain will expose
MCP tools through, mirroring `whagent_net/plugin` /
`audience_score_system/plugin`. It is a placeholder in this task — no MCP
server exists yet, so no MCP entries are registered here. A later M1 task
adds `krill/mcp` and wires `plugin/user/.mcp.json` / `mcp_config.json` to
it.
