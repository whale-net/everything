# leaflab-api — Environment Variables

> Read this when configuring, deploying, or debugging `leaflab-api`.

## Server (`leaflab-api`)

| Variable | Default | Description |
|----------|---------|--------------|
| `PORT` | `50051` | gRPC listen port |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | AMQP URL, used to publish `DeviceConfig` pushes |
| `PG_DATABASE_URL` | `""` | PostgreSQL connection string |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — see `libs/go/grpcauth`. `none` is the local-development and integration-test default; `leaflab/Tiltfile` sets no OIDC config and relies on it. |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak/OIDC realm URL; required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_OIDC_CLIENT_ID` | `""` | **This API's own** client/audience, not the UI's. `grpcauth` validates that an incoming token's `aud` includes this value, so a token minted for a different client (e.g. the leaflab UI) is rejected here unless the UI's client is configured to include this API in its access token's audience. See `libs/go/grpcauth/KEYCLOAK.md`'s audience-mapper step. |

## What's authenticated (NFR2)

Through M1, only the three read RPCs M1 adds require a valid, correctly-audienced
access token when `GRPC_AUTH_MODE=oidc`:

- `ListBoardsWithState`
- `GetBoardDetail`
- `GetSensorReadingHistory`

`PushDeviceConfig`, `GetDeviceConfig`, and `ListBoards` predate M1 and remain
unauthenticated regardless of `GRPC_AUTH_MODE`, so the operator's existing
`grpcurl`-based config-push path (`leaflab/scripts/push-config.sh`) keeps
working unchanged in every environment. **M2 brings these three inside the
fence** — do not treat their current exemption as permanent. The allowlist
lives in `leaflab/api/auth.go` (`authenticatedMethods`); it is not derived
from `libs/go/grpcauth`, which has no per-method policy (see
`leaflab/api/auth.go`'s doc comment for why that library is not extended).

### Two OIDC clients

Because `GRPC_OIDC_CLIENT_ID` is this API's own audience, an operator turning
on `GRPC_AUTH_MODE=oidc` must provision **two** OIDC clients in Keycloak: one
for the leaflab UI (or whatever caller obtains the user's token) and one for
this API. The UI's client must be configured so a signed-in user's access
token names this API's client ID in its `aud` claim — otherwise every call to
the three fenced RPCs is rejected with `codes.Unauthenticated` even from a
legitimately signed-in user. See `libs/go/grpcauth/KEYCLOAK.md`.

Standard `libs/go/logging` environment auto-detection also applies
(`APP_NAME`, `APP_DOMAIN`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_*_DISABLED`,
etc.) — see that package's doc comment for the full list.
