# leaflab/api — Environment Variables

Runtime configuration for the `leaflab-api` gRPC service. This file is filled in across
Phase 1 tasks (root plan #1166); each task documents only the variables it introduces.

## Support reference (FR80)

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_SUPPORT_REFERENCE_TTL_MINUTES` | `15` | How long a support reference (`CreateSupportReference`) stays resolvable before it expires — FR80's "short lifetime, configurable". Must be a positive integer; a malformed value fails boot, same as `LEAFLAB_ADMIN_ELEVATION_MINUTES`. |

Guessing resistance against a leaked/exhausted rate-limit budget also depends on the
code's own entropy (`leaflab/api/support_reference.go`'s `supportReferenceCodeLength`,
currently 10 symbols from a 32-symbol alphabet — 50 bits) and on
`LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_*` below, not on this TTL alone.

## Rate limiting (NFR10)

Per-principal and per-session request limits, enforced by `leaflab/api/ratelimit`'s
`InMemoryLimiter` and applied by `leaflab/api`'s rate-limit interceptor (see
`leaflab/api/ratelimit_interceptor.go`). Six named buckets exist
(`read_default`, `resend`, `ack_wait_concurrent`, `claim_open`, `claim_round`,
`support_reference_resolve`); `read_default` is wired into the interceptor chain against
every RPC, and `support_reference_resolve` is wired against `ResolveToHousehold` (FR80,
FR10.2) — the rest remain unwired pending their own RPCs. Each bucket takes a pair of
variables — a call-count limit and a window length in whole seconds — both optional; an
unset variable falls back to the bucket's default in `leaflab/api/ratelimit/env.go`'s
`DefaultConfigs`. A limit of `0` or less disables enforcement for that bucket entirely.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_LIMIT` | `120` | Max calls per principal (or per peer address for the one anonymous RPC, `GetHealth`) within the window below. Applied to every RPC. |
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `read_default`. |
| `LEAFLAB_API_RATELIMIT_RESEND_LIMIT` | `3` | FR42's re-send limit. Not yet wired to an RPC in Phase 1. |
| `LEAFLAB_API_RATELIMIT_RESEND_WINDOW_SECONDS` | `300` | Window length, in seconds, for `resend`. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_LIMIT` | `5` | FR47's concurrent open-wait limit. Not yet wired to an RPC in Phase 1. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `ack_wait_concurrent`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_LIMIT` | `5` | FR76's claim-initiation limit, keyed on the submitted `device_id` **and** the calling principal — never per-board (a per-board cap is itself an existence oracle). Not yet wired to an RPC in Phase 1. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_open`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_LIMIT` | `10` | FR76.2's claim-round limit. Not yet wired to an RPC in Phase 1. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_round`. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_LIMIT` | `10` | FR80's support-reference resolution limit, applied per admin principal against `ResolveToHousehold` (all three query kinds share the bucket — see `rateLimitBucketByMethod`'s doc comment). |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_WINDOW_SECONDS` | `60` | Window length, in seconds, for `support_reference_resolve`. |

A malformed value (not a valid integer) fails the boot the same way a malformed
`LEAFLAB_API_DEV_MODE` does — loudly, before any dependency is dialed — rather than silently
falling back to the default.

**Storage:** in-process (`leaflab/api/ratelimit.InMemoryLimiter`), not a shared store. Valid
for Phase 1 only because `leaflab-api` runs at `replicas = 1` (see `leaflab/api/BUILD.bazel`'s
`release_app`) — one process means in-process state is trivially identical across "replicas."
If `leaflab-api` is ever scaled beyond one replica, this must move to a shared store (e.g.
Redis) first, or per-replica windows silently multiply the effective limit.
