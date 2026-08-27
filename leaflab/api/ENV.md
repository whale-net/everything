# LeafLab API Environment Variables

## Rate Limiting

Rate limits are enforced per principal and per session on all read endpoints. Limits are specified in requests per second (RPS).

| Variable | Default | Description |
|----------|---------|-------------|
| `LEAFLAB_RATELIMIT_READ_RPS` | 1000 | Rate limit for all read operations (applies to every read RPC) |
| `LEAFLAB_RATELIMIT_CLAIM_INITIATE_RPS` | 100 | Rate limit for claim initiation operations (FR76) |
| `LEAFLAB_RATELIMIT_CHALLENGE_RPS` | 100 | Rate limit for challenge operations (FR76) |
| `LEAFLAB_RATELIMIT_SUPPORT_REFERENCE_RPS` | 50 | Rate limit for support reference resolution (FR80) |
| `LEAFLAB_RATELIMIT_RESEND_RPS` | 100 | Rate limit for resend operations (FR42) |
| `LEAFLAB_RATELIMIT_CONCURRENT_WAIT_RPS` | 100 | Rate limit for concurrent open bounded waits (FR47) |

### Rate Limiter Behavior

- **Per-Principal Limits**: Each principal (authenticated user or non-human holder) has an independent token bucket.
- **Per-Session Limits**: When a session ID is present, each session of the same principal has its own independent token bucket.
- **Fail-Open**: If a bucket is not found in the registry, the request is allowed.
- **Error Response**: Rate-limited requests are refused with gRPC status code `ResourceExhausted` (code 8).

### Example Configuration

```bash
# Strict limits for production
LEAFLAB_RATELIMIT_READ_RPS=500
LEAFLAB_RATELIMIT_CHALLENGE_RPS=50
LEAFLAB_RATELIMIT_SUPPORT_REFERENCE_RPS=20

# Permissive limits for development
LEAFLAB_RATELIMIT_READ_RPS=10000
LEAFLAB_RATELIMIT_CLAIM_INITIATE_RPS=1000
```

## Admin Elevation (FR10)

Cross-household reach is not standing. An admin enters elevation deliberately
against a named target household with a stated reason; it is time-boxed and
expires automatically. Renewal re-applies the same configured duration.

| Variable | Default | Description |
|----------|---------|-------------|
| `LEAFLAB_ELEVATION_DURATION_SECONDS` | 3600 | Duration of an admin elevation window (FR10, A22); 60 minutes by default |

## Support References (FR80)

A household member can produce a short-lived, opaque, revocable support
reference that an admin can resolve — in the standing lane, without
elevation — to that household.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_SUPPORT_REFERENCE_DURATION_SECONDS` | 900 | How long a support reference remains resolvable after creation (FR80); 15 minutes by default |

## Staleness Threshold (A23)

A23 governs "not reporting" classification everywhere it is used: FR79 (fleet
health listing), FR42.2 and FR62. Computed in one place — see
`leaflab/api/staleness/staleness.go`.

| Variable | Default | Description |
|----------|---------|-------------|
| `LEAFLAB_STALENESS_MULTIPLIER` | 3 | Multiplier applied to a board's longest configured poll interval (A23) |
| `LEAFLAB_STALENESS_FLOOR_SECONDS` | 900 | Minimum not-reporting threshold regardless of poll interval (A23); 15 minutes by default |

## Other Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 50051 | gRPC server port |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | RabbitMQ connection URL |
| `PG_DATABASE_URL` | (empty) | PostgreSQL connection URL (required) |
| `LEAFLAB_API_AUTH_MODE` | `none` | Authentication mode: `none`, `oidc` |
| `LEAFLAB_API_OIDC_ISSUER` | (empty) | OIDC issuer URL (required if auth mode is `oidc`) |
| `LEAFLAB_API_OIDC_CLIENT_ID` | (empty) | OIDC client ID (required if auth mode is `oidc`) |
| `LEAFLAB_API_REFLECTION_ENABLED` | `false` | Enable gRPC reflection (`true` or `false`) |
