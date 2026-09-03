# Shared Libraries — TOC

## Python Libraries (`libs/python/`)

| Library | Purpose | Docs |
|---------|---------|------|
| `alembic` | Consolidated database migration framework (no alembic.ini needed) | [README](python/alembic/README.md) |
| `cli` | Typer-based CLI framework with shared components | [README](python/cli/README.md) |
| `gunicorn` | Gunicorn server utilities | [README](python/gunicorn/README.md) |
| `logging` | OTLP-first structured logging with auto-detection | [README](python/logging/README.md) |
| `openapi_gen` | OpenAPI client code generation utilities | [README](python/openapi_gen/README.md) |
| `rmq` | RabbitMQ connection and messaging | [README](python/rmq/README.md) |
| `retry` | Retry with exponential backoff | [README](python/retry/README.md) |

## Go Libraries (`libs/go/`)

| Library | Purpose | Docs |
|---------|---------|------|
| `grpcauth` | gRPC authentication/authorization: server-side JWT interceptors and client-side credential helpers, with a `none` dev mode requiring no Keycloak | [README](go/grpcauth/README.md), [KEYCLOAK](go/grpcauth/KEYCLOAK.md) |
| `htmxauth` | HTMX authentication (OIDC + no-auth modes) | [README](go/htmxauth/README.md) |
| `htmxbase` | HTMX base template and layout utilities | [README](go/htmxbase/README.md) |
| `htmxsse` | Server-sent events over RabbitMQ for HTMX live updates with reconnect baseline suppression | [README](go/htmxsse/README.md) |
| `htmxui` | Shared HTMX UI primitives/chrome/themes (`templ_library` BUILD shape — diverges from `htmxauth`/`htmxbase`'s plain `go_library`) | [README](go/htmxui/README.md) |
| `logging` | Go structured logging | [README](go/logging/README.md) |
| `mcpauth` | OAuth2 authorization-server front end for MCP servers — RFC 9728/8414 discovery metadata, RFC 7591 dynamic client registration, and a DB-backed mint/verify/revoke/list lifecycle for the opaque bearer credential it issues; the caller's identity comes from a pluggable, already-established-session `CallerResolver`, never a fresh verification against an external IdP; schema is applied by the consuming domain's own migration | [README](go/mcpauth/README.md) |
| `migrate` | Postgres schema migration CLI/library (history tracking, rollback detection) | [README](go/migrate/README.md) |
| `rmq` | RabbitMQ connection, publishing, and consumption with automatic channel recovery | [README](go/rmq/README.md) |
| `temporal` | Temporal SDK client/worker bootstrap, env config, logging bridge | [README](go/temporal/README.md) |
