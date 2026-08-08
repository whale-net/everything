# app-registry-api

gRPC server implementing all four services in
[`../protos/api.proto`](../protos/api.proto). Built in **AR-1** (skeleton) and
filled in across **AR-2** / **AR-3**.

Not yet implemented.

## Intended layout

```
server/
  main.go             # wiring: config, db pool, auth interceptors, grpc.Serve
  handlers/           # one file per service, thin — validation + delegation
    app.go            # AppRegistry
    artifact.go       # ArtifactRegistry
    promotion.go      # PromotionRegistry
    environment.go    # EnvironmentRegistry
  repository/         # interfaces
    postgres/         # pgx implementations, SCD2 transactions
  promotability/      # deploy_unit -> Promotability rules + override checks
  idempotency/        # key storage and response replay
```

## Conventions

- Follows `manmanv2/api` for structure: `go_binary` + `release_app`, otelgrpc
  interceptors, reflection enabled.
- `libs/go/db` for the pgx pool, `libs/go/grpcauth` for OIDC, `libs/go/logging`
  for structured logs.
- Business rules live here, never in the CLI — see
  [`../ARCHITECTURE.md`](../ARCHITECTURE.md).
- Every write RPC requires an `idempotency_key` and replays stored responses.
- Promotion writes close-and-open SCD2 rows plus the audit event plus the
  writeback outbox row in **one transaction**.

## Release metadata

`app_type: internal-api`, `port: 50051`. Only reachable in-cluster and by CI —
it is not an external API.
