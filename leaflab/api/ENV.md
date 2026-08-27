# leaflab-api — Environment Variables

> Read this when configuring, deploying, or debugging the LeafLab API service.

## Server

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `PORT` | `50051` | No | gRPC listen port |
| `PG_DATABASE_URL` | — | Yes | PostgreSQL connection string |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | No | AMQP URL a config push is published to (`amq.topic` exchange, `leaflab.<device_id>.config` routing key) |

## Auth (FR11)

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LEAFLAB_API_AUTH_MODE` | `none` | No | `none` (dev bypass, injects fake Claims) or `oidc` (verifies a presented bearer token) |
| `LEAFLAB_API_DEV_MODE` | `false` | No | Must be `true` for `LEAFLAB_API_AUTH_MODE=none` to boot at all -- refused otherwise (FR11.1) |
| `LEAFLAB_API_OIDC_ISSUER` | — | When `AUTH_MODE=oidc` | OIDC issuer URL |
| `LEAFLAB_API_OIDC_CLIENT_ID` | — | When `AUTH_MODE=oidc` | OIDC client id |

## Push Validation (FR39)

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LEAFLAB_API_POLL_INTERVAL_MS_MIN` | `1000` | No | Lower bound of a valid `poll_interval_ms` on a pushed sensor entry (a payload's `poll_interval_ms == 0` means "use device default" and is exempt from this check entirely -- see `firmware/proto/config.proto`) |
| `LEAFLAB_API_POLL_INTERVAL_MS_MAX` | `3600000` | No | Upper bound of a valid `poll_interval_ms` (default: one hour) |

Both are resolved once at boot into `config.PollIntervalBounds` and refused (the process does not start) if either fails to parse as a positive `uint32`, or if min is greater than max -- `PushDeviceConfig`'s FR39 validation must never silently run with a meaningless zero-value range. See `leaflab/api/config/validate.go`'s `PollIntervalBounds` doc comment.

## Local Development (Tilt)

All values are injected from the Tiltfile. No `.env` file is needed.
