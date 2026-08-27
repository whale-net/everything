# Host Manager Resolver — Self-Updating Deployment

The resolver is a sidecar that polls App Registry for what version is
currently promoted to an environment and, when it changes, redeploys the
`host-manager` container to that version automatically. This replaces
manually re-pulling and restarting `host-manager` every time a new version
is promoted.

Binary: `//manmanv2/host-resolver`. Compose example: `compose/`.

## How it works

```
┌────────────────────────────┐
│  App Registry (control     │
│  plane, off-host)          │   PromotionRegistry.GetEnvironmentState
└──────────────┬─────────────┘   (gRPC, polled every POLL_INTERVAL)
               │
┌──────────────▼─────────────┐
│  host-manager-resolver      │
│  (this host, container)     │
│  - resolves promoted version│
│  - pulls new image          │
│  - swaps host-manager       │───── Docker Engine API over
│    container to it          │      /var/run/docker.sock
│  - health checks it         │
│  - rolls back on failure    │
└──────────────┬──────────────┘
               │ recreates
┌──────────────▼──────────────┐
│  host-manager (this host,   │
│  container)                 │
└──────────────────────────────┘
```

Each poll tick:

1. Calls `PromotionRegistry.GetEnvironmentState` for `APP_REGISTRY_ENVIRONMENT`
   and finds the entry whose artifact repository matches
   `APP_REGISTRY_REPOSITORY`.
2. Compares that version against the image tag of the currently running
   `HOST_MANAGER_CONTAINER_NAME` container (read via `docker inspect`, not
   from `.env` — see "Why not shell out to `docker compose`" below).
3. If they differ: pulls the new image, stops and removes the old
   container, creates and starts a new one with the same config (env,
   mounts, network, restart policy) but the new image, and waits for it to
   report healthy.
4. If the health check fails: recreates the container with the previous
   image and returns an error (visible in `docker logs
   manmanv2-host-manager-resolver` and via the container's own exit/retry
   behavior under `restart: always`). It does not keep retrying the *same*
   bad version in a loop — it will try again once App Registry reports a
   different version.
5. On success, rewrites `HOST_MANAGER_VERSION` in `.env` — purely so
   `cat .env` and `docker compose up -d` reflect reality; the resolver
   itself never reads this value back.

### Why not shell out to `docker compose`

An earlier design had the resolver invoke `docker compose up -d --no-deps
host-manager` from inside its own container, the way a manual redeploy
would. That requires bundling the `docker` CLI and compose plugin inside
the resolver's image, and — because Compose resolves relative paths
(`./data`, `.env`) against the *host* filesystem via the daemon, not the
resolver container's own filesystem — requires mounting the whole project
directory into the resolver at the exact same absolute path it lives at on
the host, which is easy to get wrong and silently break.

Instead the resolver talks to the Docker Engine API directly (the same
`libs/go/docker` library `host-manager` itself already uses to manage game
containers), cloning the running container's full config and swapping only
the image. This needs no CLI in the image, no path-mapping assumptions, and
mounts only `/var/run/docker.sock` and the single `.env` file it updates.

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
container — it does not know how to construct one from scratch. The first
`docker compose up -d` (with `HOST_MANAGER_VERSION` set in `.env`) must
create that container before the resolver's first poll tick needs it. If
the container doesn't exist yet, the resolver logs an error and retries
next tick rather than guessing at a config.

### Auth

`GetEnvironmentState` only needs a read-capable credential (see
`tools/app_registry/architecture/` for the PromotionRegistry role model —
reads are open to any authenticated client, writes are role-gated). This
repo does not yet provision a dedicated read-only Keycloak client id for
this purpose (existing ones are `app-registry-builder-<env>` and
`app-registry-promoter-<env>`, both write-capable) — provision one
scoped to read-only before using `GRPC_AUTH_MODE=oidc` in production. For
local testing, `GRPC_AUTH_MODE=none` matches App Registry's own dev mode.

## Configuration

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | app-registry-api address |
| `APP_REGISTRY_ENVIRONMENT` | *(required)* | Environment key to watch, e.g. `prod` |
| `APP_REGISTRY_REPOSITORY` | `ghcr.io/whale-net/manmanv2-host-manager` | Image repository to match against promoted artifacts |
| `HOST_MANAGER_CONTAINER_NAME` | `manmanv2-host-manager` | Name of the container to swap |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket path |
| `ENV_FILE_PATH` | `/workspace/.env` | Where to rewrite the deployed version for visibility |
| `VERSION_ENV_VAR` | `HOST_MANAGER_VERSION` | Key written in `ENV_FILE_PATH` |
| `POLL_INTERVAL` | `60s` | How often to check App Registry |
| `HEALTH_CHECK_TIMEOUT` | `60s` | Max time to wait for the new container to look healthy |
| `HEALTH_CHECK_POLL_INTERVAL` | `5s` | How often to poll container state during a health check |
| `GRPC_AUTH_MODE` / `GRPC_AUTH_TOKEN_URL` / `GRPC_AUTH_CLIENT_ID` / `GRPC_AUTH_CLIENT_SECRET` | `none` | Same semantics as the App Registry CLI's variables of the same name — see `tools/app_registry/ENV.md` |

## Updating the resolver itself

The resolver does not manage its own version — bump `RESOLVER_VERSION` in
`.env` and run `docker compose up -d --no-deps host-manager-resolver`
manually, the same way you'd update any other pinned-version compose
service. There is no bootstrapping problem here in the other direction:
unlike `host-manager`, its own config never needs to be cloned from a
running container.

## Security

The resolver mounts `/var/run/docker.sock`, which grants it root-equivalent
control of the host. Treat its image with the same trust level as
`host-manager` itself: only run images built from this repo, and keep the
host it runs on properly secured (same considerations as "Docker Socket
Access" in the [containerized-deployment archive](../docs/ARCHIVE/containerized-deployment.md)).
