# App Registry — Environment Variables

> All environment variables read by `app-registry-api`,
> `app-registry-migration`, `app-registry-worker`, and `app-registry-ui` —
> one `ENV.md` per domain per `AGENTS.md`; the UI does **not** get its own
> file. See "UI (`app-registry-ui`)" below.
>
> **AR-4a** added `libs/go/temporal` (client/worker bootstrap) and a
> Temporal dev server in Tilt. **AR-4b** adds the first real consumer:
> `app-registry-worker`, which drains `writeback_outbox` and runs
> `WritebackWorkflow` — see its own section below and
> [`worker/README.md`](worker/README.md).

## Database

Both the server and the migration runner read `PG_DATABASE_URL` first; the
migration runner falls back to the discrete `DB_*` variables below if it is
unset (see `libs/go/migrate`'s `DefaultConfig`). The server has no fallback —
it requires `PG_DATABASE_URL`.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `PG_DATABASE_URL` | server, migration | *(required for server)* | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname?sslmode=disable` |
| `DB_HOST` | migration | `localhost` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PORT` | migration | `5432` | Used only when `PG_DATABASE_URL` is unset |
| `DB_USER` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PASSWORD` | migration | `""` | Used only when `PG_DATABASE_URL` is unset |
| `DB_NAME` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_SSL_MODE` | migration | `disable` | Used only when `PG_DATABASE_URL` is unset |

## RabbitMQ Message Queue

> **FR0.1 & FR0.2:** Added in Phase 0 (#1113/#1125) for htmxsse event streaming.
> All three app-registry binaries (UI, worker, server) can publish events to
> the shared `app-registry.htmxsse` exchange; absent or unreachable RabbitMQ is
> non-fatal (NFR7). See `tools/app_registry/events` for exchange config and
> `tools/app_registry/ARCHITECTURE.md` for the event flow.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `RABBITMQ_URL` | server, worker, ui | *(unset)* | RabbitMQ connection URL (amqp or amqps scheme), e.g. `amqp://user:pass@host:5672/vhost`. Required format: `amqp[s]://username:password@host:port/vhost`. When unset, event publishing is skipped (NFR7: graceful degradation). |
| `RABBITMQ_SSL_VERIFY` | server, worker, ui | `true` | For amqps URLs: set to `false` to skip certificate verification (dev/test only). |
| `RABBITMQ_CA_CERT_PATH` | server, worker, ui | *(unset)* | For amqps URLs: path to custom CA certificate file for server verification. |
| `RABBITMQ_TLS_SERVER_NAME` | server, worker, ui | *(unset)* | For amqps URLs: server name for certificate verification (useful when connecting via k8s service but cert is for external domain). |

### Deployment example (Helm + secrets)

A real deployment supplies `RABBITMQ_URL` via Kubernetes secret:

\`\`\`yaml
apps:
  app-registry-server:
    secretEnv:
      - name: RABBITMQ_URL
        secretName: app-registry-broker-config
        key: rabbitmq-url
  app-registry-worker:
    secretEnv:
      - name: RABBITMQ_URL
        secretName: app-registry-broker-config
        key: rabbitmq-url
  app-registry-ui:
    secretEnv:
      - name: RABBITMQ_URL
        secretName: app-registry-broker-config
        key: rabbitmq-url
\`\`\`

The secret must be provisioned separately with scoped RabbitMQ credentials:
- Exchange: `app-registry.htmxsse` (dedicated for app-registry)
- Vhost: the vhost name from the URL (usually `app-registry-dev` for dev, `app-registry` for prod)
- Permissions: configure/write/read on that exchange and associated queues only; no configure/write on other exchanges

Local dev (Tilt) auto-provisions the vhost with `setup_rabbitmq` helper and grants blanket `.*` permissions (safe for dev, not production).

## Server (`app-registry-api`)

| Variable | Default | Description |
|----------|---------|--------------|
| `PORT` | `50051` | gRPC listen port |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — see `libs/go/grpcauth` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak/OIDC realm URL; required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience in the JWT; required when `GRPC_AUTH_MODE=oidc` |

Standard `libs/go/logging` environment auto-detection also applies
(`APP_NAME`, `APP_DOMAIN`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_*_DISABLED`,
etc.) — see that package's doc comment for the full list.

### CLI binary S3 (issue #979/#983)

`ArtifactRegistry.ResolveBinaryURL` resolves a CLI binary (`release_helper_go`
/ `app-registry`) + version + platform to a *presigned* S3 download URL,
backed by a dedicated bucket (NFR4: not a reuse of any existing bucket).

NFR2 originally called for the API server to construct *unsigned* public
URLs against a public-read bucket, needing no credentials of its own. Issue
#1101: the bucket was never actually configured to grant anonymous/public
reads, so an unsigned URL always came back `403 Forbidden` regardless of
addressing style. `ResolveBinaryURL` now presigns with its own S3
credentials instead (`libs/go/s3`'s `Client.PresignPublicGetURL`) — any
identity with read access to the object (e.g. the one that wrote it) can
hand an external consumer (CI) a working, time-limited link without
granting that consumer S3 access of its own.

| Variable | Default | Description |
|----------|---------|--------------|
| `RELEASE_TOOLS_S3_BUCKET` | `""` | Bucket name for CLI binary artifacts. Required for `ResolveBinaryURL` to return a usable URL. |
| `RELEASE_TOOLS_S3_PUBLIC_ENDPOINT` | `""` | Public-facing endpoint `ResolveBinaryURL`'s presigned URLs are addressed against, virtual-hosted-style (OVH's public endpoint rejects path-style with HTTP 400): `PresignPublicGetURL(key)` signs a request to `"<scheme>://<bucket>.<endpoint-host>/<key>"`. |
| `RELEASE_TOOLS_S3_REGION` | `""` | Region for the server-side (read/presign) `s3.Client`. |
| `RELEASE_TOOLS_S3_ACCESS_KEY` | `""` | Static access key for the server-side (read/presign) `s3.Client`. Needs read access to `RELEASE_TOOLS_S3_BUCKET` — reusing the publish side's write credentials below is sufficient. |
| `RELEASE_TOOLS_S3_SECRET_KEY` | `""` | Static secret key for the server-side (read/presign) `s3.Client`. |

The following are consumed by the *publish* side (the FinalizePublish
S3-publish task, `worker/release/finalize.go`), not by `ResolveBinaryURL` —
documented here so the full `RELEASE_TOOLS_S3_*` var set lives in one place.
Same variable names as the read-side REGION/ACCESS_KEY/SECRET_KEY above, but
these are two independently-configured deployments (`app-registry-api` vs.
`app-registry-worker`) — nothing cross-validates that their actual secret
values agree on the same bucket/region, so a region or credential mismatch
between them is a real source of drift to check if `ResolveBinaryURL` starts
failing again after this fix.

| Variable | Default | Description |
|----------|---------|--------------|
| `RELEASE_TOOLS_S3_ENDPOINT` | *(unset)* | Custom S3 endpoint (e.g. OVH, MinIO) the worker's `s3.Client` connects to when publishing CLI binaries. Used by the publish side, see the FinalizePublish S3-publish task. |
| `RELEASE_TOOLS_S3_REGION` | *(unset)* | Region for the publish-side `s3.Client`. Used by the publish side, see the FinalizePublish S3-publish task. |
| `RELEASE_TOOLS_S3_ACCESS_KEY` | *(unset)* | Static access key for the publish-side `s3.Client`. Used by the publish side, see the FinalizePublish S3-publish task. |
| `RELEASE_TOOLS_S3_SECRET_KEY` | *(unset)* | Static secret key for the publish-side `s3.Client`. Used by the publish side, see the FinalizePublish S3-publish task. |

**S3 key convention** (must match exactly between the publish side and
`ResolveBinaryURL`):
- Binary: `<binary>/<version>/<binary>-<os>-<arch>`, e.g.
  `release_helper_go/v1.2.3/release_helper_go-linux-amd64` (`os` ∈
  {`linux`,`darwin`}, `arch` ∈ {`amd64`,`arm64`}, matching
  `package_assets.go`'s `<name>-<os>-<arch>` output).
- Checksum manifest: `<binary>/<version>/checksums.txt` — one manifest per
  binary+version, covering all its platform variants, same `checksums.txt`
  format `package_assets.go`'s `generateChecksumFiles` already produces.

### Role model (AR-3a)

Server-side enforcement lives in `server/auth`; see
[`ARCHITECTURE.md`](ARCHITECTURE.md) "Authorization" for the role table and
[`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) for how
to configure the Keycloak side. In short: `AppRegistry.ReconcileApps`,
`ArtifactRegistry.RecordBuild`/`RecordArtifact` require
`app-registry-builder`; `AppRegistry.SetAppStatus` and every
`EnvironmentRegistry` write (`UpsertEnvironment`/`ArchiveEnvironment`)
require `app-registry-admin`; `PromotionRegistry.Promote`/`Rollback`
require the environment-scoped `app-registry-promoter-<environment_key>`
via `server/auth.RequirePromoter` (AR-3c); all read RPCs require only that
the caller is authenticated (any role).

**`GRPC_AUTH_MODE=none` and local/CI dev claims:** `libs/go/grpcauth`'s
dev-mode claims default to `Roles: ["admin"]`, which satisfies none of this
service's `app-registry-*` role checks. `server/main.go` overrides this via
`grpcauth.ServerConfig.DevRoles`, set to `server/auth.AllRoles()` — so in
`none` mode (the Tiltfile default, and the default for any CI path that
hasn't opted into `oidc`) every request is treated as holding every
app-registry role, and local dev / the AR-2c CI recording path keep working
unchanged. This only matters in `none` mode; `oidc` mode always uses the
token's real roles.

## CLI (`app-registry`)

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | `app-registry-api` address; overridden by `--address` |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — must match the server |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint; required when `GRPC_AUTH_MODE=oidc` (e.g. `https://auth.example.com/realms/whale/protocol/openid-connect/token`) |
| `GRPC_AUTH_CLIENT_ID` | `""` | Keycloak service-account client ID (e.g. `app-registry-builder`); required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Keycloak service-account client secret; required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_USE_TLS` / `GRPC_TLS_SKIP_VERIFY` / `GRPC_CA_CERT_PATH` / `GRPC_TLS_SERVER_NAME` | — | TLS options — see `libs/go/grpcclient` |

The CLI fetches and auto-refreshes a client-credentials token via
`grpcauth.NewServiceAccountDialOption`, the same mechanism
`manmanv2/host` and `manmanv2/log-processor` use to reach their API. See
KEYCLOAK.md section 6 "CI — GitHub Actions" for the shape of a workflow job
setting these four variables.

### CI wiring (AR-3d)

`.github/workflows/release.yml`'s recording steps and
`.github/workflows/promote.yml` set the four CLI variables above from GitHub
Actions secrets/variables — see [DEPLOY.md](DEPLOY.md) §4 and §6 for the full
placement rationale:

| GitHub Actions name | Kind | Maps to | Used by |
|---|---|---|---|
| `vars.APP_REGISTRY_ADDRESS` | Repository variable | `APP_REGISTRY_ADDRESS` | both |
| `vars.APP_REGISTRY_AUTH_TOKEN_URL` | Repository variable | `GRPC_AUTH_TOKEN_URL` | both |
| `vars.APP_REGISTRY_BUILDER_ENV` | Repository variable | `GRPC_AUTH_CLIENT_ID=app-registry-builder-<value>`, falls back to `dev` when unset | `release.yml` recording steps, plus (AR-7f) "Build helm charts with versioning" |
| `secrets.APP_REGISTRY_BUILDER_CLIENT_SECRET` | Repository secret | `GRPC_AUTH_CLIENT_SECRET` | `release.yml` recording steps, plus (AR-7f) "Build helm charts with versioning" |
| `secrets.APP_REGISTRY_PROMOTER_CLIENT_SECRET` | Environment secret, one per GitHub Environment (e.g. `promotion-dev`/`promotion-prod`) | `GRPC_AUTH_CLIENT_SECRET` (`GRPC_AUTH_CLIENT_ID=app-registry-promoter-<registry_environment>`) | `promote.yml` only |

AR-7f (issue #558) adds one more consumer of the builder credential: the
release plan step's "Build helm charts with versioning" reuses these same
four variables so `tools/release_helper_go`'s `build-helm-chart` can call
`ArtifactRegistry.CheckChartHermeticity` when `APP_REGISTRY_CICD_OPT_IN` is
`true` — see ARCHITECTURE.md "Compose-time chart hermeticity (AR-7f, issue
#558)". No new variable was introduced for this; it dials the same server
with the same credential the later steps in that job already use.

`GRPC_AUTH_MODE=oidc` is hardcoded in both workflows rather than read from a
variable — the CLI must match whatever the server runs, and the server is
expected to run `oidc` in every environment these workflows target.

`vars.APP_REGISTRY_ADDRESS` must include the port (e.g.
`dev-app-registry.whalenet.dev:443`) — `libs/go/grpcclient`'s TLS
auto-detect (`shouldUseTLS`) only enables TLS when the address contains
`:443` or starts with `https://`; a bare hostname dials plaintext against a
TLS-only ingress and fails to connect (see issue #539). The Keycloak
clients backing the builder credential are environment-scoped
(`app-registry-builder-dev` / `app-registry-builder-prod`, mirroring the
promoter clients) — there is no bare `app-registry-builder` client.

## Temporal (`libs/go/temporal`)

| Variable | Default | Description |
|----------|---------|--------------|
| `TEMPORAL_HOST` | `localhost:7233` | Temporal frontend service `host:port`. Named to match `friendly_computing_machine`'s existing `TEMPORAL_HOST` convention. |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | *(none)* | Default task queue name; no fallback. `app-registry-worker` falls back to `writeback.TaskQueue` (`"app-registry-writeback"`) itself when this is unset — see below. |

See [`libs/go/temporal/README.md`](../../libs/go/temporal/README.md) for the
client/worker API. See
[`TESTING.md`](TESTING.md#temporal-ar-4a) for exercising a local Temporal
dev server via Tilt.

## Worker (`app-registry-worker`, AR-4b)

Every variable above under Database and Temporal also applies (the worker
needs `PG_DATABASE_URL` to drain the outbox and `TEMPORAL_HOST` to run
`WritebackWorkflow`). Additionally:

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | `app-registry-api` address the stub `Writeback` activity reads state from via `GetEnvironmentState` — see `worker/writeback/stub.go`. Any authenticated credential works; that RPC requires no specific role. |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc`, for the client above — same semantics as the CLI's variable of the same name. |
| `GRPC_AUTH_TOKEN_URL` / `GRPC_AUTH_CLIENT_ID` / `GRPC_AUTH_CLIENT_SECRET` | `""` | Required when `GRPC_AUTH_MODE=oidc` — same as the CLI. |
| `WRITEBACK_OUTPUT_DIR` | `/tmp/app-registry-writeback` | Local directory the stub `Writeback` activity's `Publish` writes rendered `<environment_key>.json` documents (plus a `.state_hash` sidecar) to. Not a gitops path or S3 bucket — see `worker/README.md`, publishing anywhere is explicitly out of scope for AR-4b. |
| `WRITEBACK_BATCH_SIZE` | `20` | Max outbox rows claimed per drain pass. |
| `WRITEBACK_POLL_INTERVAL` | `5s` | Delay between drain passes (Go duration syntax, e.g. `5s`, `500ms`). |
| `WRITEBACK_CLAIM_STALE_AFTER` | `2m` | How long a `'claimed'` outbox row is left alone before a later pass reclaims it — must exceed `WritebackWorkflow`'s activity `StartToCloseTimeout` (30s) comfortably, or a still-healthy claim gets needlessly reclaimed. This is the knob that makes "worker killed mid-run" recoverable — see `worker/README.md`'s verification section. |
| `WORKER_ID` | `app-registry-worker-<hostname>` | Recorded in `writeback_outbox.claimed_by`, for operator visibility into which process holds a claim. |
| `WRITEBACK_GITOPS_REPO` | *(unset)*, required for the real `GitOpsActivities` | e.g. `whale-net/argok8s` — no default. Selects the real gitops-committing `Publish` implementation over `StubActivities` when non-empty; see `worker/writeback/gitops.go` and ARCHITECTURE.md "Writeback: outbox → Temporal". |
| `WRITEBACK_GITOPS_BRANCH` | `main` | Branch of `WRITEBACK_GITOPS_REPO` to clone/commit/push against. Only read when `WRITEBACK_GITOPS_REPO` is set. |
| `WRITEBACK_GITHUB_APP_ID` | *(unset)*, required when `WRITEBACK_GITOPS_REPO` is set | GitHub App ID used to mint an installation token for pushes — no default, deliberately not hardcoded (see "do not hardcode" in the writeback tracking issue). |
| `WRITEBACK_GITHUB_APP_INSTALLATION_ID` | *(unset)*, required when `WRITEBACK_GITOPS_REPO` is set | GitHub App installation ID for `WRITEBACK_GITOPS_REPO` — no default. |
| `WRITEBACK_GITHUB_APP_PRIVATE_KEY` | *(unset)*, required when `WRITEBACK_GITOPS_REPO` is set | Raw multi-line PEM private key for the GitHub App, used to sign the installation-token JWT — no default. Delivered via chart `secretEnv` in argok8s's own `<env>/values.yaml`, never committed here (there is no volume-mount path in `tools/helm` — see `tools/helm/README.md`'s `secretEnv` FAQ). |
| `WRITEBACK_GIT_AUTHOR_NAME` | `app-registry-writeback[bot]` | Git commit author name for gitops writes. |
| `WRITEBACK_GIT_AUTHOR_EMAIL` | `app-registry-writeback[bot]@users.noreply.github.com` | Git commit author email for gitops writes. |
| `ARGOCD_SERVER` | *(unset)*, required for the real (non-noop) ArgoCD integration | Base URL of the ArgoCD API server, e.g. `https://argocd.example.com` — no default. Consumed by `libs/go/argocd`'s `Client` (issue #1027), wired into `TriggerArgoRefresh`/`PollArgoSyncStatus` activities in a later task of the same plan. |
| `ARGOCD_AUTH_TOKEN` | *(unset)*, required alongside `ARGOCD_SERVER` | Bearer token sent as `Authorization: Bearer <token>` on every ArgoCD API call — no default. Provisioned as a worker secret; must be scoped via ArgoCD-side RBAC to least privilege (NFR1) rather than a cluster-admin credential. |

### ReleaseWorkflow (issue #889)

`ReleaseWorkflow` and its activities run on the same worker process and
task queue as `WritebackWorkflow` above (`release.TaskQueue ==
writeback.TaskQueue`) — see `worker/release/workflow.go`.
`DispatchBuild`/`PollBuild` and `ResolvePlan` are each opt-in independently
of the writeback vars above; unset, they return a clear "not configured"
error rather than running with a silently-defaulted credential or
workspace (see `worker/release/activities.go`).

| Variable | Default | Description |
|----------|---------|--------------|
| `RELEASE_GITHUB_APP_ID` | *(unset)*, required for `DispatchBuild`/`PollBuild` | GitHub App ID used to mint an installation token for the `workflow_dispatch` API call — no default. Setting this is what selects the real `GitHubDispatcher` over the "not configured" error; see `worker/release/github.go`. May reuse the same App as `WRITEBACK_GITHUB_APP_ID` (with `actions:write` permission added) or a dedicated one — implementation choice, not fixed by this repo. |
| `RELEASE_GITHUB_APP_INSTALLATION_ID` | *(unset)*, required when `RELEASE_GITHUB_APP_ID` is set | GitHub App installation ID for the repo `RELEASE_GITHUB_REPO_OWNER`/`RELEASE_GITHUB_REPO_NAME` names — no default. |
| `RELEASE_GITHUB_APP_PRIVATE_KEY` | *(unset)*, required when `RELEASE_GITHUB_APP_ID` is set | Raw multi-line PEM private key for the GitHub App above, used to sign the installation-token JWT — no default, same delivery convention as `WRITEBACK_GITHUB_APP_PRIVATE_KEY`. |
| `RELEASE_GITHUB_REPO_OWNER` | `whale-net` | Owner of the repo `release.yml` lives in. |
| `RELEASE_GITHUB_REPO_NAME` | `everything` | Repo `release.yml` lives in. |
| `RELEASE_GITHUB_WORKFLOW_FILE` | `release-v2.yml` | Workflow file `DispatchBuild`/`PollBuild` dispatch/poll. Must stay `release-v2.yml` in any deployment sending the `resolved_plan` input (issue #927) -- `release.yml` (v1) doesn't declare that input and 422s (`Unexpected inputs provided: ["resolved_plan"]`) since #931 restored it to human-trigger-only during the v2 migration. |
| `RELEASE_GITHUB_REF` | `main` | Git ref `DispatchBuild` dispatches `RELEASE_GITHUB_WORKFLOW_FILE` against. |
| `RELEASE_PLAN_BINARY_PATH` | `release_helper_go` (resolved via `PATH`) | Path to the `release_helper_go` binary `ResolvePlan`'s interim CLI shell-out invokes — see `worker/release/plan.go`'s package doc comment for why this is a shell-out rather than an in-process library call. Reused by `FinalizePublish`'s `finalize-app`/`finalize-chart` shell-outs (issue #928, `worker/release/finalize.go`) — same binary, one flag. The worker's own image (`worker/BUILD.bazel`'s `release_app`) bundles this binary at `/usr/local/bin/release_helper_go` via `additional_tars` (`//tools/release_helper_go:release_helper_go_tar`), which the PATH-resolved default already finds — this var only needs setting to point at a different binary. |
| `RELEASE_WORKSPACE_ROOT` | *(unset)* — optional | Working directory `ResolvePlan` runs `RELEASE_PLAN_BINARY_PATH plan` from. **No longer required to be a full monorepo checkout.** `ResolvePlan` resolves each target's Domain/Name/AppType via the registry itself and passes it as `--apps-metadata`/`--charts-metadata` (bazel-free — see `worker/release/plan.go`'s package doc comment); when unset, it uses a fresh scratch temp directory per invocation. `FinalizePublish` does **not** read this variable at all: its `finalize-app`/`finalize-chart` shell-outs always run against their own `os.MkdirTemp("", "release-finalize-")` scratch directory (`worker/release/finalize.go`) rather than a configurable workspace, since neither needs a real git checkout (retag uses `--repository`/`--digest`; chart packaging uses an absolute `--chart-dir` under that scratch dir) — see `finalize.go`'s package doc comment and `activities.go`'s `Activities.WorkspaceRoot` field doc comment, which both spell out that `FinalizePublish` reuses `RELEASE_PLAN_BINARY_PATH` but not this var. Only `helm` needs to be on `PATH` for `FinalizePublish` (bundled into the worker's image via `//tools/helm_cli:helm_cli_tar`) — `git` is not required by `finalize-app`/`finalize-chart`. |

### FinalizePublish (issue #928)

`FinalizePublish` runs after `PollBuild` reports the merged release-trigger GHA job (`release-v2.yml`'s `build-release-artifacts`) succeeded, and before `VerifyPublished` — see `worker/release/finalize.go`'s package doc comment for the full design (why this step exists, how build outputs get from GHA back to Temporal, and the credential-locality reasoning below). It reuses `RELEASE_GITHUB_APP_*`/`RELEASE_GITHUB_REPO_*`/`RELEASE_PLAN_BINARY_PATH` above unchanged, plus:

| Variable | Default | Description |
|----------|---------|--------------|
| `RELEASE_CHART_REPO_URL` | `https://charts.whalenet.dev` | ChartMuseum repository URL `finalize-chart` uploads packaged charts to. |
| `RELEASE_CHART_REPO_USER` | *(unset)* | ChartMuseum username. **Credential-locality move (issue #928):** previously a GitHub Actions secret (`secrets.CHART_REPO_USER`) held by `release-v2.yml`'s chart-release job; ChartMuseum write access now lives only here, on the worker — the merged `build-release-artifacts` GHA job holds no ChartMuseum credential at all. |
| `RELEASE_CHART_REPO_PASS` | *(unset)* | ChartMuseum password — same credential-locality move as above (was `secrets.CHART_REPO_PASS`). |
| `RELEASE_GHCR_TOKEN` | *(unset)*, required whenever the batch has app targets | Classic PAT (`write:packages`, `read:packages`) for a dedicated bot/machine GHCR account, threaded to `finalize-app`'s subprocess as `GHCR_TOKEN`. Required for `finalize-app`'s registry-side retag (via `crane.Tag`, see `tools/release_helper_go/cmd/releaser_ghcr_retag.go`); `FinalizePublish` fails fast with a clear error if unset and the batch includes any app target. |

**GHCR retag credential (issue #996):** a GitHub App installation token cannot write to organization-owned GHCR packages when used outside a GitHub Actions run — a hard GitHub product limitation, not a permissions/scope gap (see issue #996 for the confirmed repro and GitHub community reports). `finalize-app`'s retag therefore uses the static `RELEASE_GHCR_TOKEN` bot-account PAT above, not a token minted from `RELEASE_GITHUB_APP_*` — `worker/release/activities.go`'s `Activities.GHCRToken` field, populated from `RELEASE_GHCR_TOKEN` in `worker/main.go`, passed through unchanged from `worker/release/finalize.go`. `RELEASE_GITHUB_APP_*` is unaffected and still required for `DispatchBuild`/`PollBuild` and for `FinalizePublish`'s own GitHub Actions artifact-download calls (`ListRunArtifacts`/`DownloadArtifact`) — none of those touch GHCR.

The API server (`app-registry-api`) also needs Temporal connectivity as of
issue #889 (`TriggerRelease` starts `ReleaseWorkflow` directly) — the same
Database/Temporal variables above apply there too, not just to the worker.

### Artifact reaper (AR-7b, issue #558)

The stale-row reaper (`worker/reaper`) runs as a third loop in the same
`app-registry-worker` process, alongside the Temporal worker and the outbox
drainer, sweeping `artifact` rows stuck in `allocated`/`publishing` to
`failed` — see ARCHITECTURE.md "The reaper is not optional" and
`worker/README.md`.

| Variable | Default | Description |
|----------|---------|--------------|
| `ARTIFACT_REAPER_TIMEOUT` | `30m` | How long an `artifact` row may sit in `allocated` or `publishing` (measured from `state_changed_at`) before the next sweep moves it to `failed` with `fail_reason = 'stale'`. Go duration syntax. **AR-7d (issue #558): `state_changed_at` is stamped once, at plan time, for every target in a release run** — `release.yml`'s `plan-release` job calls `BeginPublishBatch` for the whole matrix before it fans out (see ARCHITECTURE.md "The run log" -> "As built (AR-7d)"), so a target whose matrix leg hasn't started yet has been "publishing" since plan time, not since its own push began. `release.yml`'s per-leg `Begin publish (image)` step re-arms this clock (a `publishing -> publishing` heartbeat) immediately before that leg's own push, and revives (`failed -> publishing`) a row the reaper already expired while the leg was still queued — so a reap that races a slow-to-schedule leg does not lose the eventual push, but it does cost that leg a transient, misleading `failed` state in `app-registry builds status` until it runs. **Set this comfortably longer than the WHOLE release run** (every matrix leg's cross-arch image build, end to end, plus queueing delay) **, not just the slowest individual leg** — a value sized only for one leg reaps every target that hasn't started yet almost immediately after plan time. |
| `ARTIFACT_REAPER_POLL_INTERVAL` | `5m` | Delay between sweep passes (Go duration syntax). Coarser than `WRITEBACK_POLL_INTERVAL` — this is a background hygiene sweep, not a redelivery loop reacting to a worker crash. |

## UI (`app-registry-ui`)

HTMX admin UI (FR-47/48/49). Records promotions; never deploys anything
(NFR-1). Reads from `os.Getenv` only, no config files — see `ui/README.md`
for structure/styling and `ui/main.go`'s `LoadConfig`, the source of record
for defaults below.

| Variable | Default | Description |
|----------|---------|--------------|
| `HOST` | `0.0.0.0` | Bind address |
| `PORT` | `8000` | HTTP port |
| `AUTH_MODE` | `none` | HTTP authentication mode: `none` or `oidc` |
| `SECRET_KEY` | `dev-secret-key-change-in-production` | Session encryption key; also derives the AES key used to encrypt refresh tokens at rest |
| `REGISTRY_API_URL` | `app-registry-api:50051` | `app-registry-api` gRPC endpoint |
| `GRPC_AUTH_MODE` | `none` | gRPC token-forwarding mode: `none` or `oidc` — should match `app-registry-api`'s own `GRPC_AUTH_MODE` |
| `PG_DATABASE_URL` | *(required, no fallback)* | PostgreSQL connection string for `htmxauth`'s DB-backed session store (the `ui_sessions` table) — **same variable name as the rest of App Registry** (see Database above), deliberately not `DATABASE_URL` |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` / `OIDC_REDIRECT_URI` | `""` | Required when `AUTH_MODE=oidc` — OIDC provider URL, client ID/secret, and callback URL |
| `APP_REGISTRY_UI_SHOW_DEMO_DOMAIN` | `false` | Issue #750: when `false` (default), the Apps Catalog (screen 20) hides every app/chart whose domain is `demo` — the same `demoDomain` (`release_scope.go`) release.yml's `include_demo` input and `resolveReleaseScope`'s `includeDemo` param exclude by default. Set to `true`/`1` (parsed via `strconv.ParseBool`) to show them. Only gates the catalog listing; a demo app's detail page (`/apps/{full_name}`) remains reachable by direct URL either way. |
| `OIDC_POST_LOGOUT_REDIRECT_URI` | `""` | Optional. Where the OIDC provider sends the browser after RP-initiated logout (issue #763). `htmxauth` derives a default from `OIDC_REDIRECT_URI`'s origin when unset; set this only if that derived value isn't a registered post-logout redirect URI with the provider (Keycloak validates it against the client's `post.logout.redirect.uris`) |
| `APP_REGISTRY_SSE_HEARTBEAT_INTERVAL` | `5s` | Issue #1537. Worst-case staleness for the Promotion Details page's SSE stream when a broker-pushed update for the promotion being watched never arrives (dropped/never-published event, or `RABBITMQ_URL` unset entirely — see RabbitMQ Message Queue above). Broker-pushed updates are unaffected and still land immediately. Parsed via `time.ParseDuration` (e.g. `10s`, `1m`); an unparseable value falls back to the default. Overrides `htmxsse.DefaultConfig()`'s own 30s default — see `ui/main.go`'s `initializeSSEHub`. |

**`PG_DATABASE_URL` points at the same database as the registry's
`PG_DATABASE_URL` above, and is used *solely* for session storage.** The
UI never issues SQL against registry domain tables (`apps`, `environments`,
`promotions`, etc.) — all registry domain data comes over gRPC from
`app-registry-api`, forwarding the logged-in user's own access token
(FR-40, `libs/go/grpcauth`). The UI's only direct database use is
`libs/go/htmxauth`'s session store.

Because `htmxauth` names the `ui_sessions` table **unqualified** (no schema
prefix in any query — see `libs/go/htmxauth/db_session.go`), the connection
string's `search_path` is part of the contract: `PG_DATABASE_URL` must
resolve to the **same schema** `app-registry-migration`'s `011_...` migration
writes `ui_sessions` into, or every session read/write silently misses the
table (or, worse, hits an unrelated same-named table on a different
schema-per-tenant setup). Don't point the UI at the registry database via a
role/search_path that differs from the migration runner's.

**An unset `PG_DATABASE_URL` is a hard failure, not a degraded mode.**
`NewApp` in `ui/main.go` refuses to start without it — this UI always uses
DB-backed sessions and never falls back to cookie-only sessions (unlike
`manmanv2/ui`), because cookie sessions cannot refresh OIDC access tokens.
A missing `ui_sessions` table (migration not yet run) also fails boot, via
`htmxauth.NewDBSessionManager`'s preflight probe, rather than surfacing as a
mysterious runtime 500 on first login.

**Deploying for real:** `SECRET_KEY`, `OIDC_CLIENT_SECRET`, and
`PG_DATABASE_URL` must never be set as literal `env:` values in a values.yaml
override that ships to a real cluster — source them via `secretKeyRef`
(`tools/helm`'s `secretEnv`, added in #635) instead:

```yaml
apps:
  app-registry-ui:
    secretEnv:
      - name: SECRET_KEY
        secretName: app-registry-ui-secrets
        key: secret-key
      - name: OIDC_CLIENT_SECRET
        secretName: app-registry-ui-secrets
        key: oidc-client-secret
      - name: PG_DATABASE_URL
        secretName: app-registry-ui-secrets
        key: database-url
```

`tools/app_registry:app_registry_chart`'s app key for the UI is
`app-registry-ui` (`<domain>-<name>`, matching every other app in this
chart). See `tools/helm/README.md`'s "How do I add environment variables?"
FAQ for the full secretEnv/envFrom mechanism, and DEPLOY.md for the rest of
this service's deployment checklist.

## Local Development (Tilt)

```bash
# Enable/disable services (default: true)
ENABLE_APP_REGISTRY_MIGRATION=true
ENABLE_APP_REGISTRY_API=true
ENABLE_APP_REGISTRY_WORKER=true
ENABLE_APP_REGISTRY_UI=true
ENABLE_TEMPORAL=true

# Infrastructure — set to 'custom' to use an external Postgres
BUILD_POSTGRES_ENV=default       # or 'custom'
PG_DATABASE_URL=postgres://...   # if BUILD_POSTGRES_ENV=custom
```

Local access (`tilt up` from `tools/app_registry/`): API forwarded to
`localhost:50061`, UI forwarded to `localhost:8090`, Postgres to
`localhost:5432`, Temporal gRPC to `localhost:7233` and Web UI to
`localhost:8233`. The UI's `PG_DATABASE_URL` is set by the Tiltfile from the
**same** `pg_database_url` value fed to the API/migration/worker above — one
local database, not two (see "UI" above). `AUTH_MODE=none` and
`GRPC_AUTH_MODE=none` in Tilt, same as the API. The worker has no forwarded
port (it serves nothing) — inspect it via `tilt logs app-registry-worker` or
a shell into its pod (`WRITEBACK_OUTPUT_DIR` lives inside the container).
`ENABLE_APP_REGISTRY_WORKER=true` with `ENABLE_TEMPORAL=false` skips the
worker (it has nothing to poll) with a printed warning rather than deploying
into a guaranteed-crash loop.

## Postgres MCP (AGY and Claude Code plugin)

`mcp_config.json` at the plugin root (`tools/app_registry/mcp_config.json`, exposed
for AGY via `.agents/plugins/app-registry` and symlinked to `.mcp.json` for Claude
Code — see `.claude-plugin/marketplace.json`) wires up three read-restricted
(`--access-mode=restricted`) crystaldba `postgres-mcp` servers via `uvx`, one
per environment:

| Server | Connection |
|---|---|
| `app-registry-pg-tilt` | Hardcoded to the Tiltfile default (`postgres://postgres:password@localhost:5432/app_registry`) — matches `setup_postgres`'s local credentials, not a secret |
| `app-registry-pg-dev` | `APP_REGISTRY_DEV_DATABASE_URI` (shell env var, not set by default) |
| `app-registry-pg-prod` | `APP_REGISTRY_PROD_DATABASE_URI` (shell env var, not set by default) |

These are separate from `PG_DATABASE_URL` above (which the server/migration
processes read) so that dev and prod can be queried side by side from the
same AGY or Claude Code session without swapping a single variable.

`postgres-mcp`'s `pyproject.toml` declares an unpinned `mcp[cli]>=1.25.0`
dependency; `uvx` resolves that to `mcp==2.x`, which dropped
`mcp.server.fastmcp` and breaks `postgres-mcp` at import time
(`ModuleNotFoundError: No module named 'mcp.server.fastmcp'`). Each server's
`args` therefore pin it down with `--with "mcp<2"` before `postgres-mcp`.
See [crystaldba/postgres-mcp#187](https://github.com/crystaldba/postgres-mcp/issues/187).
If reconnecting still fails with the same error, `uv`'s package cache may
need a `uv cache clean postgres-mcp` / `uv cache clean mcp`.
