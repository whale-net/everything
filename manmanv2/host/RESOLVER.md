# Host Manager Resolver — Self-Updating Deployment

`host-manager`'s adoption of [`//tools/compose-resolver`](../../tools/compose-resolver/README.md),
an app-agnostic sidecar that polls App Registry for what version is
currently promoted to an environment and redeploys one compose service to
that version automatically. This replaces manually re-pulling and
restarting `host-manager` every time a new version is promoted.

For how the resolver actually works (the poll/pull/swap/health-check/
rollback cycle, why it talks to the Docker Engine API instead of shelling
out to `docker compose`, and the full config reference), see
[`tools/compose-resolver/README.md`](../../tools/compose-resolver/README.md).
This doc only covers what's specific to running it against `host-manager`.

Compose example: [`compose/`](compose/).

```
┌────────────────────────────┐
│  App Registry (control     │
│  plane, off-host)          │   PromotionRegistry.GetEnvironmentState
└──────────────┬─────────────┘   (gRPC, polled every POLL_INTERVAL)
               │
┌──────────────▼─────────────┐
│  host-manager-resolver      │
│  (this host, container)     │
└──────────────┬──────────────┘
               │ recreates, via Docker Engine API over
               │ /var/run/docker.sock
┌──────────────▼──────────────┐
│  host-manager (this host,   │
│  container)                 │
└──────────────────────────────┘
```

## Adopting it

1. `cp compose/.env.example compose/.env`, fill in the values, including an
   initial `HOST_MANAGER_VERSION` (the resolver only takes over *after*
   `host-manager` exists as a container — see "Bootstrap" below).
2. `cd compose && docker compose up -d` — brings up both `host-manager` and
   `host-manager-resolver`.
3. Promote a new `manmanv2-host-manager` version in App Registry for
   `APP_REGISTRY_ENVIRONMENT` and watch `docker logs -f
   manmanv2-host-manager-resolver`.

### Bootstrap

The resolver clones its config from the *currently running* `host-manager`
container — it does not know how to construct one from scratch (see
`tools/compose-resolver/README.md`). The first `docker compose up -d`
(with `HOST_MANAGER_VERSION` set in `.env`) must create that container
before the resolver's first poll tick needs it.

### Auth

`host-manager` and the resolver each hold their own Keycloak client
credential, kept as separate `.env` variables
(`GRPC_AUTH_CLIENT_*` for `host-manager`'s connection to the ManMan
control-plane API, `APP_REGISTRY_GRPC_AUTH_CLIENT_*` for the resolver's
connection to App Registry — see `compose/docker-compose.yml`). Do not
point both at the same client id/secret: they authenticate to different
services and, in most Keycloak setups, different realms/audiences.

This repo does not yet provision a dedicated read-only Keycloak client id
for the resolver (existing App Registry client ids —
`app-registry-builder-<env>` and `app-registry-promoter-<env>` — are both
write-capable). Provision one scoped to read-only before using
`APP_REGISTRY_GRPC_AUTH_MODE=oidc` in production. For local testing,
`APP_REGISTRY_GRPC_AUTH_MODE=none` matches App Registry's own dev mode.

## Security

The resolver mounts `/var/run/docker.sock`, which grants it root-equivalent
control of the host. Treat its image with the same trust level as
`host-manager` itself: only run images built from this repo, and keep the
host it runs on properly secured (same considerations as "Docker Socket
Access" in the [containerized-deployment archive](../docs/ARCHIVE/containerized-deployment.md)).
