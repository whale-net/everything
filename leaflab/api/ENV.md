# leaflab/api — Environment Variables

Runtime configuration for the `leaflab-api` gRPC service. This file is filled in across
Phase 1 tasks (root plan #1166); each task documents only the variables it introduces.

## Rate limiting (NFR10)

Per-principal and per-session request limits, enforced by `leaflab/api/ratelimit`'s
`InMemoryLimiter` and applied by `leaflab/api`'s rate-limit interceptor (see
`leaflab/api/ratelimit_interceptor.go`). Six named buckets exist
(`read_default`, `resend`, `ack_wait_concurrent`, `claim_open`, `claim_round`,
`support_reference_resolve`); Phase 1 wires only `read_default` into the interceptor chain
against every RPC. Each bucket takes a pair of variables — a call-count limit and a window
length in whole seconds — both optional; an unset variable falls back to the bucket's default
in `leaflab/api/ratelimit/env.go`'s `DefaultConfigs`. A limit of `0` or less disables
enforcement for that bucket entirely.

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
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_LIMIT` | `10` | FR80's support-reference resolution limit. Not yet wired to an RPC in Phase 1. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_WINDOW_SECONDS` | `60` | Window length, in seconds, for `support_reference_resolve`. |

A malformed value (not a valid integer) fails the boot the same way a malformed
`LEAFLAB_API_DEV_MODE` does — loudly, before any dependency is dialed — rather than silently
falling back to the default.

**Storage:** in-process (`leaflab/api/ratelimit.InMemoryLimiter`), not a shared store. Valid
for Phase 1 only because `leaflab-api` runs at `replicas = 1` (see `leaflab/api/BUILD.bazel`'s
`release_app`) — one process means in-process state is trivially identical across "replicas."
If `leaflab-api` is ever scaled beyond one replica, this must move to a shared store (e.g.
Redis) first, or per-replica windows silently multiply the effective limit.

## Board claim — possession challenge (FR76)

A28's constants for the self-service board claim flow (`leaflab/api/claim.Config`,
`leaflab/api/claim/config.go`), all configurable via a pair of env vars per value — a
count/duration and, for durations, the unit is always whole seconds. An unset variable falls
back to `leaflab/api/claim.DefaultConfig`. Scaffold-phase (task #1342): not yet wired into
`leaflab/api/main.go` — the RPCs (`OpenClaimChallenge`, `MarkClaimRound`,
`GetClaimChallengeStatus`, `CompleteClaim`) have no handler in `server.go` yet.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_API_CLAIM_ROUNDS_REQUIRED` | `2` | A28's `r`: distinct challenger-marked restarts required to discharge a challenge. **Enforced at startup to be `>= 2`** — the requirement text's explicit floor; a lower value fails boot loudly rather than silently weakening the challenge. |
| `LEAFLAB_API_CLAIM_ROUND_BOUND_SECONDS` | `180` (3 minutes) | How long after a round's `t0` (set by `MarkClaimRound`) a restart signal still counts for that round. |
| `LEAFLAB_API_CLAIM_CHALLENGE_LIFETIME_SECONDS` | `900` (15 minutes) | Total bounded lifetime of a challenge from `OpenClaimChallenge` (requirement 8: "long enough to walk to the greenhouse and back"). |
| `LEAFLAB_API_CLAIM_ATTEMPTS_PER_ROUND` | `2` | How many times a single round may be re-marked before the challenge is exhausted. |
| `LEAFLAB_API_CLAIM_COOLDOWN_SECONDS` | `1800` (30 minutes) | How long a `(principal, device_id)` pair spends in `claim_cooldown` after a challenge ends not-discharged. **Not an A28-specified number** — the requirement text names "cooldown after failure" but gives no duration; this default is this task's own choice, flagged per the issue's residual-risk caveat. |
| `LEAFLAB_API_CLAIM_RESTART_UPTIME_THRESHOLD_SECONDS` | `300` (5 minutes) | The processor's restart-detection threshold (Implementation phase, `leaflab/processor/handler.go`): an `uptime_s` regression below this value is treated as a genuine restart; a larger drop is presumed to be the `uint32` millisecond wrap at ~49.7 days and must not count (requirement 4). Not one of A28's five named constants, but grouped here as the same kind of env-overridable configuration. |

A malformed value (not a valid integer) fails the boot the same way a malformed rate-limit
variable does — loudly, before any dependency is dialed.

**Schema:** `leaflab/migrate/migrations/021_claim_challenge.up.sql` — `claim_challenge`,
`claim_challenge_round`, `claim_cooldown` (short-lived, expiring rows; explicitly **not** SCD2,
NFR6.3 — no `valid_to` column), and `board_uptime_watermark` (the processor's per-board current
`uptime_s` watermark used to detect a restart).
