# compose-resolver

A resolver sidecar for single-host `docker-compose` deployments: it polls
App Registry for the version currently promoted to an environment and,
when it changes, redeploys one compose service to that version
automatically — pull, swap the container, health check, roll back on
failure. It's app-agnostic; point it at any one image repository and
container name.

Binary: `//tools/compose-resolver`. For a worked example (host-manager's
own adoption of this), see
[`manmanv2/host/RESOLVER.md`](../../manmanv2/host/RESOLVER.md) and
[`manmanv2/host/compose/`](../../manmanv2/host/compose/).

## How it works

Each poll tick:

1. Calls `PromotionRegistry.GetEnvironmentState` for `APP_REGISTRY_ENVIRONMENT`
   and finds the entry whose artifact repository matches
   `APP_REGISTRY_REPOSITORY`.
2. Compares that version against the image tag of the currently running
   `CONTAINER_NAME` container (read via `docker inspect`, not from `.env` —
   see "Why not shell out to `docker compose`" below).
3. If they differ: pulls the new image, stops and removes the old
   container, creates and starts a new one with the same config (env,
   mounts, network, restart policy) but the new image, and waits for it to
   report healthy.
4. If the health check fails: recreates the container with the previous
   image and returns an error (visible in the resolver's own logs). It does
   not keep retrying the *same* bad version in a loop — it tries again once
   App Registry reports a different version.
5. On success, rewrites `VERSION_ENV_VAR` in `ENV_FILE_PATH` — purely so a
   human running `cat .env` or `docker compose up -d` sees the version
   that's actually running; the resolver itself never reads this value
   back.

### Why not shell out to `docker compose`

The natural design is a resolver that rewrites `.env` and runs
`docker compose up -d --no-deps <service>`. That requires bundling the
`docker` CLI and compose plugin inside the resolver's own image, and —
because Compose resolves relative paths (`./data`, `.env`) against the
*host* filesystem via the daemon, not the container the CLI runs in —
requires mounting the whole project directory into the resolver at the
exact same absolute path it lives at on the host, which is easy to get
wrong and silently break.

Instead the resolver talks to the Docker Engine API directly (via
`libs/go/docker`), cloning the running container's full config and
swapping only the image. This needs no CLI in the image, no path-mapping
assumptions, and mounts only `/var/run/docker.sock` and the single `.env`
file it updates.

One consequence: the target container must already exist (brought up once
by a human via `docker compose up -d`) before the resolver's first
reconcile tick, since it clones config from the running container rather
than synthesizing one from the compose file.

## Configuration

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | app-registry-api address |
| `APP_REGISTRY_ENVIRONMENT` | *(required)* | Environment key to watch, e.g. `prod` |
| `APP_REGISTRY_REPOSITORY` | *(required)* | Image repository to match against promoted artifacts, e.g. `ghcr.io/whale-net/manmanv2-host-manager` |
| `CONTAINER_NAME` | *(required)* | Name of the running container to swap |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket path |
| `ENV_FILE_PATH` | `/workspace/.env` | Where to rewrite the deployed version for visibility |
| `VERSION_ENV_VAR` | `IMAGE_VERSION` | Key written in `ENV_FILE_PATH` |
| `POLL_INTERVAL` | `60s` | How often to check App Registry |
| `HEALTH_CHECK_TIMEOUT` | `60s` | Max time to wait for the new container to look healthy |
| `HEALTH_CHECK_POLL_INTERVAL` | `5s` | How often to poll container state during a health check |
| `LOG_SERVICE_NAME` | `compose-resolver` | Service name tag on structured logs |
| `LOG_DOMAIN` | `""` | Domain tag on structured logs |
| `LOG_FORMAT` | `json` | `json` or anything else for text |
| `GRPC_AUTH_MODE` / `GRPC_AUTH_TOKEN_URL` / `GRPC_AUTH_CLIENT_ID` / `GRPC_AUTH_CLIENT_SECRET` | `none` | Same semantics as the App Registry CLI's variables of the same name — see `tools/app_registry/ENV.md` |

### Health checks

If the target image defines a Docker `HEALTHCHECK`, "healthy" means
`State.Health.Status == "healthy"`. Otherwise it means the container has
stayed `running` — with restart count unchanged — for one full
`HEALTH_CHECK_POLL_INTERVAL`, the best available signal that it isn't
crash-looping when the app has no real health endpoint.

### Auth

`GetEnvironmentState` is a public read path in App Registry — no
credential required, by design, regardless of App Registry's own
`GRPC_AUTH_MODE` (see `tools/app_registry/architecture/13-authorization.md`
"Public Read Paths", issue #853: deployment tools are meant to query
promotion state without provisioning Keycloak credentials, the same way
you'd pull from a container registry anonymously). `GRPC_AUTH_MODE=none`
(the default here) reflects that.

If you still want the resolver to authenticate — e.g. defense in depth —
`GRPC_AUTH_MODE=oidc` plus the other `GRPC_AUTH_*` vars work the same as
every other client in this repo, but this repo doesn't provision a
read-scoped client id for it (existing ones — `app-registry-builder-<env>`
and `app-registry-promoter-<env>` — are both write-capable), and it buys
you nothing `GetEnvironmentState` doesn't already grant anonymously.

## Security

The resolver mounts `/var/run/docker.sock`, which grants it root-equivalent
control of the host. Treat its image with the same trust level as whatever
it's deploying: only run images built from this repo, and keep the host it
runs on properly secured.

## Updating the resolver itself

The resolver does not manage its own version — bump its image tag in
whatever compose file deploys it and re-run `docker compose up -d`
manually, the same way you'd update any other pinned-version compose
service. There's no bootstrapping problem in the other direction: unlike
the container it manages, its own config never needs to be cloned from a
running container.
