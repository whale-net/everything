# leaflab/api — Environment Variables

Runtime configuration for the `leaflab-api` gRPC service. This file is filled in across
Phase 1, Phase 2, and Phase 4 tasks (root plan #1166); each task documents only the variables
it introduces.

> Read this when configuring, deploying, or debugging `leaflab-api`.

## Core

| Variable | Default | Required | Description |
|----------|---------|----------|--------------|
| `PORT` | `50051` | No | gRPC listen port. |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | No | AMQP URL used to publish `PushDeviceConfig` messages onto `amq.topic` (same broker the processor consumes from — see `../ARCHITECTURE.md`). |
| `PG_DATABASE_URL` | — | Yes | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/leaflab?sslmode=disable`. Schema is provisioned by `leaflab-migrate` before the API starts. |

## Auth (FR11, FR11.1, FR12)

| Variable | Default | Required | Description |
|----------|---------|----------|--------------|
| `LEAFLAB_API_AUTH_MODE` | `none` | No | `none` or `oidc`, per `libs/go/grpcauth`. `none` injects fake dev `Claims` (subject `dev-user`, role `leaflab-admin` — see `main.go`'s `DevRoles`) with no token required; `oidc` validates `Authorization: Bearer <token>` via local JWKS. |
| `LEAFLAB_API_DEV_MODE` | `false` | No | Boot-time gate (FR11.1): `LEAFLAB_API_AUTH_MODE=none` is refused at startup unless this is explicitly `true` — auth mode is never inferred from an issuer URL, hostname, or `ENVIRONMENT`. Also controls whether gRPC server reflection is registered (see the A30 exposure gate note below): reflection is only ever on when `LEAFLAB_API_DEV_MODE=true`. A malformed (non-boolean) value fails boot loudly rather than silently defaulting. |
| `LEAFLAB_API_OIDC_ISSUER` | — | Required when `LEAFLAB_API_AUTH_MODE=oidc` | OIDC issuer URL, e.g. `https://auth.example.com/realms/whale`. Must match the realm `leaflab-ui` and `push-config.sh`'s device-flow credential authenticate against — see `../README.md`'s "Pushing device config" section and `libs/go/grpcauth/KEYCLOAK.md`. |
| `LEAFLAB_API_OIDC_CLIENT_ID` | — | Required when `LEAFLAB_API_AUTH_MODE=oidc` | Expected token audience. |

`leaflab-admin` (FR12) is a realm role recorded as eligibility only in Phase 1 — no RPC
branches on it yet beyond the acting-subject audit log (NFR12). See `auth.go`'s `RoleAdmin`
doc comment.

## Exposure gate (A30 — "Phase 1 must not reach production users")

Phase 1 shipped authentication with no household ownership: every authenticated caller could
see every board. Phase 2 added household scoping (FR5), closing that specific gap, but
**non-exposure is still enforced structurally, not left to convention:**

- `leaflab-api`'s `release_app` in `BUILD.bazel` declares `app_type = "internal-api"`. Per
  `tools/helm/APP_TYPES.md`, `internal-api` apps get no public Ingress by default — reaching
  this service from outside the cluster requires an operator to opt in
  (`exposeIngress: true`), which is not done for `leaflab-api` today.
- gRPC server reflection — which would let an unauthenticated caller discover the full RPC
  surface — is gated behind `LEAFLAB_API_DEV_MODE=true` (see above) and off by default (FR11.1).
  `push-config.sh` and other callers resolve the service contract from the published
  descriptor set instead of reflection; see `../README.md`.

No environment variable exists solely to gate production exposure — the gate is the absence
of an Ingress plus `LEAFLAB_API_DEV_MODE`'s default. If `leaflab-api` is ever deployed behind a
public Ingress, deployment must also confirm household scoping (FR5, Phase 2) and admin
elevation (FR10, Phase 2 — see "Admin elevation" below) are both wired for every entity-access
RPC before removing the gate; see root plan #1166's A30 note and task #1351 (NFR1.b).

## Rate limiting (NFR10)

Per-principal and per-session request limits, enforced by `leaflab/api/ratelimit`'s
`InMemoryLimiter` and applied by `leaflab/api`'s rate-limit interceptor (see
`leaflab/api/ratelimit_interceptor.go`). Six named buckets exist
(`read_default`, `resend`, `ack_wait_concurrent`, `claim_open`, `claim_round`,
`support_reference_resolve`). Each bucket takes a pair of variables — a call-count limit and a
window length in whole seconds — both optional; an unset variable falls back to the bucket's
default in `leaflab/api/ratelimit/env.go`'s `DefaultConfigs`. A limit of `0` or less disables
enforcement for that bucket entirely.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_LIMIT` | `120` | Max calls per principal (or per peer address for the one anonymous RPC, `GetHealth`) within the window below. Applied to every RPC via the generic per-method interceptor (Phase 1). |
| `LEAFLAB_API_RATELIMIT_READ_DEFAULT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `read_default`. |
| `LEAFLAB_API_RATELIMIT_RESEND_LIMIT` | `3` | FR42's re-send limit. Wired into the generic per-method interceptor chain via `rateLimitBucketByMethod` (Phase 4, task #1374) against `ResendDeviceConfig`. |
| `LEAFLAB_API_RATELIMIT_RESEND_WINDOW_SECONDS` | `300` | Window length, in seconds, for `resend`. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_LIMIT` | `5` | FR47's concurrent open-wait limit. Wired into the generic per-method interceptor chain via `rateLimitBucketByMethod` (Phase 4, task #1373) against `AwaitConfigAck`. |
| `LEAFLAB_API_RATELIMIT_ACK_WAIT_CONCURRENT_WINDOW_SECONDS` | `60` | Window length, in seconds, for `ack_wait_concurrent`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_LIMIT` | `5` | FR76's claim-initiation limit, keyed on the submitted `device_id` **and** the calling principal — never per-board (a per-board cap is itself an existence oracle). Wired directly by `OpenClaimChallenge` (Phase 2, task #1342) — see "Board claim" below. |
| `LEAFLAB_API_RATELIMIT_CLAIM_OPEN_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_open`. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_LIMIT` | `10` | FR76.2's claim-round limit. Wired directly by `MarkClaimRound` (Phase 2, task #1342) — see "Board claim" below. |
| `LEAFLAB_API_RATELIMIT_CLAIM_ROUND_WINDOW_SECONDS` | `3600` | Window length, in seconds, for `claim_round`. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_LIMIT` | `10` | FR80's support-reference resolution limit, per admin principal. Wired into the generic per-method interceptor chain (Phase 2, task #1346) against `ResolveToHousehold` — applied to that RPC's whole method (all three query kinds: person identifier, support reference, partial device id), not only the support-reference branch. |
| `LEAFLAB_API_RATELIMIT_SUPPORT_REFERENCE_RESOLVE_WINDOW_SECONDS` | `60` | Window length, in seconds, for `support_reference_resolve`. |

A malformed value (not a valid integer) fails the boot the same way a malformed
`LEAFLAB_API_DEV_MODE` does — loudly, before any dependency is dialed — rather than silently
falling back to the default.

**Storage:** in-process (`leaflab/api/ratelimit.InMemoryLimiter`), not a shared store. Valid
only because `leaflab-api` runs at `replicas = 1` (see `leaflab/api/BUILD.bazel`'s
`release_app`) — one process means in-process state is trivially identical across "replicas."
If `leaflab-api` is ever scaled beyond one replica, this must move to a shared store (e.g.
Redis) first, or per-replica windows silently multiply the effective limit.

## Board claim — possession challenge (FR76)

A28's constants for the self-service board claim flow (`leaflab/api/claim.Config`,
`leaflab/api/claim/config.go`), all configurable via a pair of env vars per value — a
count/duration and, for durations, the unit is always whole seconds. An unset variable falls
back to `leaflab/api/claim.DefaultConfig`. Wired into `leaflab/api/main.go` (task #1342): the
RPCs (`OpenClaimChallenge`, `MarkClaimRound`, `GetClaimChallengeStatus`, `CompleteClaim`) have
handlers in `server.go`, and `claim_open`/`claim_round` are enforced directly by those two
handlers (composite `principal + device_id`/`challenge_handle` keys — see server.go's
`claimOpenRateLimitKey`/`claimRoundRateLimitKey` — rather than through the generic per-method
interceptor, which only ever derives a principal-only key).

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_API_CLAIM_ROUNDS_REQUIRED` | `2` | A28's `r`: distinct challenger-marked restarts required to discharge a challenge. **Enforced at startup to be `>= 2`** — the requirement text's explicit floor; a lower value fails boot loudly rather than silently weakening the challenge. |
| `LEAFLAB_API_CLAIM_ROUND_BOUND_SECONDS` | `180` (3 minutes) | How long after a round's `t0` (set by `MarkClaimRound`) a restart signal still counts for that round. |
| `LEAFLAB_API_CLAIM_CHALLENGE_LIFETIME_SECONDS` | `900` (15 minutes) | Total bounded lifetime of a challenge from `OpenClaimChallenge` (requirement 8: "long enough to walk to the greenhouse and back"). |
| `LEAFLAB_API_CLAIM_ATTEMPTS_PER_ROUND` | `2` | How many times a single round may be re-marked before the challenge is exhausted. |
| `LEAFLAB_API_CLAIM_COOLDOWN_SECONDS` | `1800` (30 minutes) | How long a `(principal, device_id)` pair spends in `claim_cooldown` after a challenge ends not-discharged. **Not an A28-specified number** — the requirement text names "cooldown after failure" but gives no duration; this default is this task's own choice, flagged per the issue's residual-risk caveat. |
| `LEAFLAB_API_CLAIM_RESTART_UPTIME_THRESHOLD_SECONDS` | `300` (5 minutes) | The processor's restart-detection threshold (`leaflab/processor/handler.go`/`config.go`, read directly by the processor binary — same variable name shared with `leaflab-api` as the single source of truth, no code coupling): an `uptime_s` regression below this value is treated as a genuine restart; a larger drop is presumed to be the `uint32` millisecond wrap at ~49.7 days and must not count (requirement 4). Not one of A28's five named constants, but grouped here as the same kind of env-overridable configuration. |
| `LEAFLAB_API_CLAIM_MAX_CONCURRENT_OPEN_CHALLENGES` | `3` | Requirement 2's "bounded number of concurrent open challenges per principal" — distinct from the structural one-open-per-(principal, device_id) uniqueness the schema enforces. **Not an A28-specified number**, same residual-risk flag as the cooldown duration above. |

A malformed value (not a valid integer) fails the boot the same way a malformed rate-limit
variable does — loudly, before any dependency is dialed.

**Schema:** `leaflab/migrate/migrations/021_claim_challenge.up.sql` — `claim_challenge`,
`claim_challenge_round`, `claim_cooldown` (short-lived, expiring rows; explicitly **not** SCD2,
NFR6.3 — no `valid_to` column), and `board_uptime_watermark` (the processor's per-board current
`uptime_s` watermark used to detect a restart).

**Flagged residual gap — the non-retained-manifest evidence class (requirement 4's narrow
exception) is never satisfied.** `evidence_class = 'manifest_exception'` is valid in the schema
but no code path ever writes it: the processor consumes MQTT traffic bridged through RabbitMQ's
`amq.topic` AMQP exchange (`leaflab/processor/main.go`'s `consumer.BindExchange`), and neither
`amqp091-go`'s `Delivery` type nor the AMQP 0-9-1 wire protocol it speaks carries any equivalent
of MQTT's retain flag — there is nothing in the message this processor receives that
distinguishes a live `DeviceManifest` publish from a broker-replayed retained one. Per the
issue's explicit instruction ("if it cannot be distinguished, the exception must be implemented
as never satisfied rather than always satisfied"), `leaflab/processor/handler.go`'s
`handleManifest` never calls into round-satisfaction for the manifest evidence class at all — a
challenge against a device with zero readings ever can still discharge, but only via the
`uptime_regression` evidence class once readings start arriving, never via manifest alone.
Distinguishing retained-vs-live would require the processor to speak native MQTT instead of
AMQP, or RabbitMQ to expose the flag some other way (e.g. a header) — out of this task's scope;
flagged for a follow-up rather than smoothed over.

## Admin elevation (FR10.1, FR12 activation)

The standing admin lane (`ResolveToHousehold`, FR10.2) requires no configuration beyond
`leaflab-admin` realm-role eligibility (FR12 — see `leaflab/api/auth.go`'s `RoleAdmin`). Only
the elevated lane's time-box duration is configurable.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_ADMIN_ELEVATION_MINUTES` | `60` | FR10.1's "60 minutes, configurable" elevation window, applied by `Elevate`/`RenewElevation` (`admin_elevation.expires_at = started_at + this duration`). Overrides `DefaultElevationDuration` (`leaflab/api/server.go`) when set. Must be a positive integer number of minutes; a malformed value fails the boot the same way a malformed rate-limit variable does. |

**Schema:** `leaflab/migrate/migrations/029_admin_elevation.up.sql` — `admin_elevation`. Short-
lived with an explicit expiry, explicitly **not** SCD2 (NFR6.3): a single bounded episode with
a hard end (expiry or explicit `EndElevation`), not a current-value history.

## Owner-initiated support reference (FR80)

A household member produces a short-lived, opaque, revocable code that an admin resolves, in
the standing lane (FR10.2), to that household — without disclosing any household data or
requiring a problem description at creation time.

| Variable | Default | Description |
|----------|---------|--------------|
| `LEAFLAB_SUPPORT_REFERENCE_TTL_MINUTES` | `15` | FR80's "short lifetime (configurable)" for a support reference, applied by `CreateSupportReference` (`support_reference.expires_at = created_at + this duration`). Overrides `DefaultSupportReferenceTTL` (`leaflab/api/server.go`) when set. Must be a positive integer number of minutes; a malformed value fails the boot the same way a malformed rate-limit variable does. |

**Schema:** `leaflab/migrate/migrations/032_support_reference.up.sql` — `support_reference`.
Short-lived with an explicit expiry, explicitly **not** SCD2 (NFR6.3) — same reasoning as
`admin_elevation` above. `code_hash` stores a hash of the opaque code, never the code itself —
the plaintext is not recoverable from the database.

## Local Development (Tilt)

All values are injected from `../Tiltfile`. No `.env` file is needed. Every variable
documented above that isn't set here (all of Phase 2/4's rate-limit, claim, admin-elevation,
and support-reference variables) runs on its documented default in local dev — no override is
required to exercise those features against `tilt up`.

```bash
PORT=50051
RABBITMQ_URL=amqp://rabbit:password@rabbitmq-dev.leaflab-local-dev.svc.cluster.local:5672/
PG_DATABASE_URL=postgres://postgres:password@postgres-dev.leaflab-local-dev.svc.cluster.local:5432/leaflab?sslmode=disable
LEAFLAB_API_AUTH_MODE=none
LEAFLAB_API_DEV_MODE=true
```
