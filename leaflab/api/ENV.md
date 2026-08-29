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

## Admin elevation (FR10.1)

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LEAFLAB_ADMIN_ELEVATION_MINUTES` | `60` | No | Overrides `DefaultElevationDuration` -- how long an `Elevate`/`RenewElevation` grant lasts. Must be a positive integer. |

## Push Validation (FR39)

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LEAFLAB_API_POLL_INTERVAL_MS_MIN` | `1000` | No | Lower bound of a valid `poll_interval_ms` on a pushed sensor entry (a payload's `poll_interval_ms == 0` means "use device default" and is exempt from this check entirely -- see `firmware/proto/config.proto`) |
| `LEAFLAB_API_POLL_INTERVAL_MS_MAX` | `3600000` | No | Upper bound of a valid `poll_interval_ms` (default: one hour) |

Both are resolved once at boot into `config.PollIntervalBounds` and refused (the process does not start) if either fails to parse as a positive `uint32`, or if min is greater than max -- `PushDeviceConfig`'s FR39 validation must never silently run with a meaningless zero-value range. See `leaflab/api/config/validate.go`'s `PollIntervalBounds` doc comment.

## Rate limiting (NFR10)

Per-principal and per-session request limits, enforced by `leaflab/api/ratelimit`'s
`InMemoryLimiter` and applied by `leaflab/api`'s rate-limit interceptor (see
`leaflab/api/ratelimit_interceptor.go`). Six named buckets exist
(`read_default`, `resend`, `ack_wait_concurrent`, `claim_open`, `claim_round`,
`support_reference_resolve`); Phase 1 wires only `read_default` into the interceptor chain
against every RPC, with `ack_wait_concurrent` wired in Phase 4 against `AwaitConfigAck`. Each
bucket takes a pair of variables — a call-count limit and a window length in whole seconds —
both optional; an unset variable falls back to the bucket's default in
`leaflab/api/ratelimit/env.go`'s `DefaultConfigs`. A limit of `0` or less disables enforcement
for that bucket entirely.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_LIMIT` | `120` | Max calls per principal (or per peer address for the one anonymous RPC, `GetHealth`) within the window below. Applied to every RPC. |
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `read_default`. |
| `LEAFLAB_API_RATELIMIT_RESEND_LIMIT` | `3` | FR42's re-send limit. Not yet wired to an RPC. |
| `LEAFLAB_API_RATELIMIT_RESEND_WINDOW_SECONDS` | `300` | Window length, in seconds, for `resend`. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_LIMIT` | `5` | FR47's concurrent open-wait limit, enforced against `AwaitConfigAck`. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `ack_wait_concurrent`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_LIMIT` | `5` | FR76's claim-initiation limit, keyed on the submitted `device_id` **and** the calling principal — never per-board (a per-board cap is itself an existence oracle). Not yet wired to an RPC. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_open`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_LIMIT` | `10` | FR76.2's claim-round limit. Not yet wired to an RPC. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_round`. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_LIMIT` | `10` | FR80's support-reference resolution limit. Not yet wired to an RPC. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_WINDOW_SECONDS` | `60` | Window length, in seconds, for `support_reference_resolve`. |

A malformed value (not a valid integer) fails the boot the same way a malformed
`LEAFLAB_API_DEV_MODE` does — loudly, before any dependency is dialed — rather than silently
falling back to the default.

**Storage:** in-process (`leaflab/api/ratelimit.InMemoryLimiter`), not a shared store. Valid
only because `leaflab-api` runs at `replicas = 1` (see `leaflab/api/BUILD.bazel`'s
`release_app`) — one process means in-process state is trivially identical across "replicas."
If `leaflab-api` is ever scaled beyond one replica, this must move to a shared store (e.g.
Redis) first, or per-replica windows silently multiply the effective limit.

## Local Development (Tilt)

All values are injected from the Tiltfile. No `.env` file is needed.
