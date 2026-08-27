# leaflab/api — Environment Variables

Runtime configuration for the `leaflab-api` gRPC service. This file is filled in across
Phase 1 tasks (root plan #1166); each task documents only the variables it introduces.

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

Phase 1 ships authentication with no household ownership: every authenticated caller can see
every board. **Non-exposure is enforced structurally, not left to convention:**

- `leaflab-api`'s `release_app` in `BUILD.bazel` declares `app_type = "internal-api"`. Per
  `tools/helm/APP_TYPES.md`, `internal-api` apps get no public Ingress by default — reaching
  this service from outside the cluster requires an operator to opt in
  (`exposeIngress: true`), which is not done for `leaflab-api` today.
- gRPC server reflection — which would let an unauthenticated caller discover the full RPC
  surface — is gated behind `LEAFLAB_API_DEV_MODE=true` (see above) and off by default (FR11.1).
  `push-config.sh` and other callers resolve the service contract from the published
  descriptor set instead of reflection; see `../README.md`.

No environment variable exists solely to gate production exposure — the gate is the absence
of an Ingress plus `LEAFLAB_API_DEV_MODE`'s default. If Phase 1 is ever deployed behind a
public Ingress, A30 additionally requires either a non-production-only deployment, an
allowlist of internal principals, or a feature gate — none of which exist as of Phase 1 (see
root plan #1166's A30 note); that is deliberately out of scope until Phase 2's household
scoping (FR5) lands.

## Local Development (Tilt)

All values are injected from `../Tiltfile`. No `.env` file is needed.

```bash
PORT=50051
RABBITMQ_URL=amqp://rabbit:password@rabbitmq-dev.leaflab-local-dev.svc.cluster.local:5672/
PG_DATABASE_URL=postgres://postgres:password@postgres-dev.leaflab-local-dev.svc.cluster.local:5432/leaflab?sslmode=disable
LEAFLAB_API_AUTH_MODE=none
LEAFLAB_API_DEV_MODE=true
```
