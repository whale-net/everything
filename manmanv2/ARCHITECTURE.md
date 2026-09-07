# ManManV2 — Architecture

Split-plane design, component relationships, and data flow. For vision,
personas, capability map, and the milestone roadmap, see
[PRODUCT.md](PRODUCT.md); for local development setup, see
[README.md](README.md); for all environment variables, see [ENV.md](ENV.md).

| Plane | Location | Responsibility |
|-------|----------|----------------|
| **Control Plane** | Cloud (K8s, Helm chart `manmanv2_chart`) | Orchestration, state storage, user-facing APIs and UI |
| **Execution Plane** | Bare Metal | Host manager + resolver sidecar, game server containers |

---

## System Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            CONTROL PLANE (Cloud)                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │  control-api │  │     ui       │  │    event     │  │ log-         │     │
│  │  (gRPC 50051 │  │ (HTTP 8000,  │  │  processor   │  │ processor    │     │
│  │  + REST gw)  │  │  HTMX)       │  │ (RabbitMQ    │  │ (gRPC 50053) │     │
│  │              │  │              │  │  consumer)   │  │              │     │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘     │
│         │                 │                 │                 │             │
│         └────────┬────────┴────────┬────────┴─────────────────┘             │
│                  │                 │                                         │
│         ┌────────▼─────────┐  ┌────▼────────────┐                            │
│         │    PostgreSQL    │  │    RabbitMQ     │────► S3 (log archival,     │
│         │    (domain data) │  │ (manman,        │      backups)              │
│         └──────────────────┘  │  external,      │                            │
│                               │  manmanv2.      │                            │
│                               │  htmxsse)       │                            │
│                               └─────────────────┘                            │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                              RabbitMQ (commands / status)
                                        │
┌───────────────────────────────────────▼─────────────────────────────────────┐
│                        EXECUTION PLANE (Bare Metal)                          │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │  host-manager + host-manager-resolver sidecar                       │   │
│  │  - Manages Docker containers via Docker SDK                         │   │
│  │  - Renders configuration (config strategies, env templates)         │   │
│  │  - Orchestrates Workshop addon installs, takes local backups        │   │
│  │  - Recovers/re-attaches to game containers on restart               │   │
│  │  - Self-updates via resolver polling App Registry promotion         │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│              │                    │                    │                    │
│      attach  │            attach  │            attach  │                    │
│              │                    │                    │                    │
│  ┌───────────▼──────┐ ┌──────────▼────────┐ ┌────────▼──────────┐         │
│  │  Game Server     │ │  Game Server      │ │  Game Server      │         │
│  │  Container       │ │  Container        │ │  Container        │         │
│  └──────────────────┘ └───────────────────┘ └───────────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Deployables

| Deployable | Location | Description |
|------------|----------|-------------|
| `control-api` | K8s (cloud) | gRPC API (:50051) + REST gateway; owns all domain state |
| `event-processor` | K8s (cloud) | RabbitMQ consumer syncing host/session status to Postgres; republishes to `external` + `manmanv2.htmxsse` exchanges |
| `log-processor` | K8s (cloud) | Real-time log streaming fan-out (gRPC :50053) + optional S3 archival |
| `manmanv2-ui` | K8s (cloud) | Operator UI (HTTP :8000), Go + templ + HTMX, DB-backed sessions via `libs/go/htmxauth` |
| `control-migration` | K8s (cloud, job) | Database migration runner (`migrate/`, uses `libs/go/migrate`) |
| `host-manager` | Bare metal (Docker) | Host server manager; container lifecycle, config rendering, Workshop installs, backups |
| `host-manager-resolver` | Bare metal (sidecar) | Polls App Registry promotion and redeploys `host-manager` automatically — see [host/RESOLVER.md](host/RESOLVER.md) |

The five cloud services ship together in the `manmanv2_chart` Helm chart
(root `BUILD.bazel`); `host-manager` ships separately for bare metal and
self-updates via the resolver rather than Helm. The host manager runs as a
Docker container with `/var/run/docker.sock` mounted (no privileged mode
needed); the resolver sidecar mounts the same socket and documents that
trust level explicitly in `host/RESOLVER.md`.

---

## Components

- **control-api** (`api/`) — Go/Postgres. ~100 RPCs across `protos/api.proto`
  (games, game configs, servers, deployments/SGCs, sessions, backups, backup
  configs, action definitions/executions, patches, volumes, registration,
  logs, pending restarts) plus a separate `protos/workshop.proto` (Workshop
  search/library/installations). Also hosts the `SessionRestartConsumer` and
  `PendingRestartReaper` (see [Pending Restarts](#pending-restarts)) and
  republishes the SSE trigger events the UI consumes.
- **event-processor** (`processor/`) — RabbitMQ consumer persisting
  host/session status to Postgres, session state-machine validation, stale
  host detection with auto-recovery, backup scheduling, and republishing of
  internal events onto the `external` exchange (downstream integrations) and
  the `manmanv2.htmxsse` exchange (UI live rows). See
  [processor/README.md](processor/README.md).
- **manmanv2-ui** (`ui/`) — Go + templ + HTMX UI backed by
  `libs/go/htmxui`/`libs/go/htmxauth`; Chrome/nav and shared components come
  from the shared library. Talks to control-api, stores browser sessions in
  Postgres, and holds a dedicated RabbitMQ connection for live-row SSE
  triggers (see [Event Processor → UI (Live Status)](#event-processor--ui-live-status)).
  See [ui/README.md](ui/README.md) and [ui/DESIGN_SYSTEM.md](ui/DESIGN_SYSTEM.md).
- **log-processor** (`log-processor/`) — gRPC :50053. Real-time log fan-out
  to viewers, `GetLogHistogram`/`GetHistoricalLogs`, and S3 window archival
  gated on `PG_DATABASE_URL`+`S3_BUCKET`+`API_ADDRESS`. See
  [log-processor/README.md](log-processor/README.md).
- **host-manager** (`host/`) — Runs on each bare-metal host. Manages game
  containers directly (create, attach stdin/stdout, stop, label-based
  recovery), self-registers with the control plane (TLS-capable), publishes
  status/health/logs to RabbitMQ, renders configuration (config strategies +
  env templates), orchestrates Workshop addon installs, and takes local
  backups. See [host/DEPLOYMENT.md](host/DEPLOYMENT.md) and
  [host/RESOLVER.md](host/RESOLVER.md).
- **control-migration** (`migrate/`) — Migration runner over the embedded
  SQL in `migrate/migrations/` (currently `001`–`036`).
- **Shared** — `models/` (database models, package `manmanv2/models`),
  `events/` (RabbitMQ exchange identity shared by event-processor and the
  UI), `protos/` (wire contracts).

---

## Data Model

### Entity Relationships

```
┌─────────────┐         ┌─────────────┐
│   Server    │         │    Game     │
├─────────────┤         ├─────────────┤
│ server_id   │         │ game_id     │
│ name        │         │ name        │
│ status      │         │ steam_app_id│
│ environment │         │ metadata    │
│ last_seen   │         └──────┬──────┘
│ host_public │                │ has many
│   _address  │                ▼
└──────┬──────┘         ┌──────────────┐
       │                │  GameConfig  │
       │ deploys        ├──────────────┤
       ▼                │ config_id    │
┌──────────────────┐    │ game_id (FK) │
│ ServerGameConfig │◄───│ name         │
├──────────────────┤    │ image        │
│ sgc_id           │    │ entrypoint   │
│ server_id (FK)   │    │ args_template│
│ game_config_id   │    │ env_template │
│ port_bindings    │    │ files        │
│ status           │    └──────┬──────┘
└────────┬─────────┘           │ has many
         │ has many            ▼
         │              ┌─────────────────┐
         │              │   Volumes /     │
         │              │ ConfigStrategies│
         │              │ + Patches       │
         │              └─────────────────┘
         ▼
┌─────────────────┐
│    Session      │
├─────────────────┤
│ session_id      │
│ sgc_id (FK)     │
│ started_at      │
│ ended_at        │
│ exit_code       │
│ status          │
└─────────────────┘
```

Schema lives in `migrate/migrations/` (currently `001`–`036`). Notable
constraints confirmed in schema/code:

- **No uniqueness on `(server_id, game_config_id)`** in
  `server_game_configs` — deploying the same GameConfig to one host twice is
  allowed by design. The only conflict guard is `server_ports`'
  `UNIQUE(sgc_id, server_id, port, protocol)`.
- **`workshop_installations` is `UNIQUE(sgc_id, addon_id)`** — SGC-scoped.
  The planned move to GC-level library inheritance (C28, M6 in
  [PRODUCT.md](PRODUCT.md)) is a migration off this key, not new UI.
- **Container identity is entirely label-based** — see
  [Container Identity & Orphan Recovery](#container-identity--orphan-recovery).

### Configuration Layering

There is one canonical env/render mechanism **in code** and one **accepted
but not yet built** expansion of it:

- **In code today:** container env is built solely from
  `GameConfig.env_template` at session start (`host/main.go`); config files
  render through the `ConfigurationStrategy`/`ConfigurationPatch` system
  (DB + API + host renderer all present — strategy types, patch levels,
  patch formats, per-volume overrides).
- **Accepted, not scheduled:** expanding that same patch system to
  per-deployment env overrides behind a convenience API
  (`GetEffectiveEnv`/`SetSGCEnvOverrides`) — Option B of
  [docs/DESIGN_SGC_ENV_OVERRIDES.md](docs/DESIGN_SGC_ENV_OVERRIDES.md),
  which also retires the dead migration-`006` typed parameter tables (those
  tables have no Go references and must not be revived or imitated; a flat
  `env_overrides` map must not be added as a fourth mechanism — this is
  LB4 in [PRODUCT.md](PRODUCT.md)).

### Port Management

`server_ports` rows (`server_id`, `port`, `protocol`, optional `sgc_id`,
optional `session_id`) record allocation. Port allocated to one
ServerGameConfig at a time; multiple sessions can use a port sequentially,
not concurrently. The API enforces allocation atomically during
`DeployGameConfig`/`DeleteServerGameConfig`; eventual consistency elsewhere
is acceptable.

### Pending Restarts

`pending_restarts` (migration `036_pending_restarts`) gives "a Start is
pending for this deployment, gated on session `<id>`'s Stop reaching a
terminal status" a durable, control-plane-local home. It is control-plane
state only — restarting does not add a new wire routing key or payload
field.

- **`status`**: `pending` → `started` → `failed` | `expired` (a `pending`
  row can also fail directly, without ever being claimed — see below).
- At most one `pending` row per `server_game_config_id`, enforced by a unique
  partial index (`pending_restarts_one_pending_per_sgc`), not application
  logic — this is the DB-level idempotency guard.
- Claiming a row (transition to `started`) and expiring stalled rows
  (transition to `expired` past `stall_deadline`) are each a single atomic
  `UPDATE ... RETURNING`, so concurrent callers resolve a row exactly once.

**Dispatch half — `RestartDeployment` RPC (`manmanv2/api/handlers/session.go`,
control-api):** the entry point that creates a `pending_restarts` row. Given
a `server_game_config_id`:
1. No live session → degenerates to an inline `StartSession` call (there is
   no lost-intent gap to make durable in this case), returning
   `started_session`.
2. Live session present → `Create`s the `pending_restarts` row (status
   `pending`, `gating_session_id` = the live session, `stall_deadline` = now
   + `RESTART_STALL_TIMEOUT`, see `ENV.md`) and only *after* that commits
   dispatches `StopSession`, returning `stopping_session`. Ordering is
   load-bearing: recording before dispatching is what closes the pod-dies-
   between-them window FR9 exists for. `ErrPendingRestartExists` short-
   circuits to `already_in_flight: true` with no second Stop dispatch — the
   DB unique index above is the actual enforcement, this is just surfacing
   it as an idempotent no-op.
   - If the `StopSession` dispatch itself fails, the row is moved straight
     from `pending` to `failed` (`MarkFailed` — see below) rather than left
     to sit until the reaper expires it.

This RPC is additive to `StartSession`/`StopSession` — it dispatches through
their existing handler logic rather than re-deriving command construction or
config/volume resolution, and does not change `command.*` routing keys or
`status.session.*` semantics. `manmanv2/ui`'s `restartDeployment` dispatches
a single `RestartDeployment` RPC and holds no restart state of its own — the
older goroutine-based stop-then-start is gone (#1733).

**Trigger half — `SessionRestartConsumer` (`manmanv2/api/handlers/session_restart_consumer.go`,
control-api):** a second, independent `status.session.#` consumer inside
control-api, on its own dedicated queue (`control-api.session.restart`) —
distinct from event-processor's `processor-events` queue, so the two never
compete for the same message. Two consumers now bind `status.session.#` on
the shared `"manman"` exchange (event-processor's persistence consumer and
this one); they are independent, and neither substitutes for the other —
event-processor keeps sole ownership of persisting status, and this consumer
never publishes to `status.session.*` or `manmanv2.htmxsse`.

It lives in control-api rather than event-processor because firing the
deferred Start means calling `SessionHandler.StartSession`, which is
control-api's own handler logic (config/volume resolution, active-session
checks) — routing that call through event-processor would mean either
duplicating that logic or adding a new cross-service RPC. It reuses the same
`SessionHandler` instance as the gRPC API (via a narrow `DeferredStarter`
interface), so it shares that handler's `CommandPublisher`/`workshop.Manager`
rather than constructing a second one.

On each `status.session.#` message, non-terminal statuses
(`pending`/`starting`/`running`/`stopping`) are dropped with no DB access —
the overwhelming majority of this binding's traffic. For a terminal status
(`stopped`/`crashed`/`lost`), it calls `ClaimForSession(gating_session_id)`:
that single atomic `UPDATE ... WHERE status='pending' ... RETURNING` *is*
the idempotency guarantee — a redelivered or duplicated terminal message
finds the row already `started` and claims nothing, so `StartSession` is
called at most once per pending restart. The row is claimed before
`StartSession` is attempted (a bounded, per-call context — not the
consumer's long-lived one — so a hung Start can't wedge the queue), which is
a deliberate at-most-once trade: a crash between claim and Start leaves a
`started` row with no session, rather than risk two sessions racing to start
against the same `server_game_config_id`. A `StartSession` failure moves the
row straight to `failed` with a WARNING log; nothing here retries it
automatically.

`MarkFailed` moves a row to `failed` from either `pending` (dispatch-half
failure above) or `started` (deferred-Start failure) — both are terminal
failures of the same intent and share one transition.

**Stall bound half — `PendingRestartReaper` (`manmanv2/api/handlers/pending_restart_reaper.go`,
control-api):** the trigger half above only ever resolves a row on the happy
path — a gating Stop that reaches a terminal status, which
`SessionRestartConsumer` observes. A Stop that never converges (host manager
gone, container wedged) leaves the row `pending` forever with nothing to
resolve it: the "stuck pending forever" failure FR11 exists to prevent,
merely relocated from a goroutine into a table. `PendingRestartReaper` is
that resolver, and it is a deliberately different mechanism from the trigger
half: a plain ticker, not another `status.session.#` consumer.

This is intentionally *not* event-driven. #1712's NFR9 ("not a periodic
sweep") scopes only to the normal-path dispatch — `RestartDeployment` and
`SessionRestartConsumer` above, which never poll. NFR12 explicitly permits a
time-based safety net for the stall case, since there is no event that
signals "this Stop is never coming" to drive an event-driven equivalent.
Two mechanisms, two jobs: the consumer resolves the row the instant a
terminal status arrives; the reaper is the backstop for when one never does.

Each tick calls `ExpireStalled(now)` — one atomic
`UPDATE ... WHERE status='pending' AND stall_deadline <= now RETURNING`, so
concurrent `control-api` replicas ticking at the same time each expire a
given row exactly once, the same idempotency shape as `ClaimForSession`
above. For every row it expires, the reaper logs one WARNING — WARNING per
`AGENTS.md` § Logging Levels, since the system kept going but the
operator's deployment is not running and nothing else will surface that
without this log. `ExpireStalled` erroring is logged at ERROR and the tick is
skipped; the goroutine itself never dies, so the next tick retries. Zero
expired rows is silent. The reaper only ever expires rows — it never
dispatches a `StartSession`, never retries one, and never touches a row that
isn't `pending`; an expired row is terminal, and the operator re-issues
`RestartDeployment` for another attempt (FR10, at-most-once).

Interval (`RESTART_REAPER_INTERVAL`, default `10s`) and stall timeout
(`RESTART_STALL_TIMEOUT`, default `45s`) are independent env vars — see
`ENV.md`. Worst-case stall-detection latency is their sum (~55s at the
defaults). The interval must stay well below the timeout or the bound is
meaningless — enforced only by review, not code.

**Read half — `ListPendingRestarts` RPC (`manmanv2/api/handlers/session.go`,
control-api):** the operator-visibility counterpart to the three
mechanisms above. `manmanv2/ui`'s `/sessions` deployment row reads this
table back (via `PendingRestartRepository.GetLatestBySGCIDs`) and renders a
badge distinguishing `pending` (in progress) from `failed`/`expired`
(stalled/failed) — see `manmanv2/ui/README.md` § "Restart State
Visibility" for the badge mapping and the UI-side read path. One batched RPC
per page render (every rendered SGC id in one call), never one per row.
`GetLatestBySGCIDs` excludes a resolved record older than
`pendingRestartVisibilityWindow` (5 minutes, `manmanv2/api/repository/postgres/pending_restart.go`)
so a stale terminal badge doesn't outlive its usefulness. Strictly
read-only: this RPC never writes to `pending_restarts`.

**Not SCD2:** this table intentionally does not use `valid_from`/`valid_to`
(see `AGENTS.md` § SCD2). A pending restart is a short-lived work intent with
its own terminal state machine (`status` + `resolved_at`), not dimension
history — SCD2's "current value = row with `valid_to IS NULL`" model doesn't
fit a record that is created once and resolved exactly once.

---

## Communication Patterns

### Control Plane ↔ Host Manager

| Direction | Protocol | Use Case |
|-----------|----------|----------|
| CP → Host | RabbitMQ | Commands (start, stop, configure, workshop install) |
| Host → CP | RabbitMQ | Status updates, health, logs, registration |

**Message Types (Topic Exchange, shared `"manman"` exchange):**
- `command.*` - Control commands
- `status.host.*` - Host-level status
- `status.session.*` - Session-level status
- `health.*` - Health/keepalive

These routing-key shapes and payload fields are additive-only — this is the
fleet compatibility surface (LB1 in [PRODUCT.md](PRODUCT.md)); the fleet
runs mixed host-manager versions during any rollout by construction.

Two consumers bind `status.session.#`: event-processor (persistence, on
`processor-events`) and control-api's `SessionRestartConsumer` (deferred
restarts, on `control-api.session.restart`). They are independent queues and
neither substitutes for the other.

### Host Manager ↔ Game Containers

| Direction | Mechanism | Use Case |
|-----------|-----------|----------|
| Host → Game | Docker attach (stdin) | Send commands to game |
| Game → Host | Docker attach (stdout/stderr) | Stream game output |

The host attaches to each game container via the Docker API. Stdin is written
directly to the container's attached connection. Stdout/stderr are demuxed from
the same stream using Docker's 8-byte multiplexed header format. Crash
detection is EOF on the attached stream. This direct-attach model is
load-bearing for session lifecycle, not just log streaming (LB2 in
[PRODUCT.md](PRODUCT.md)).

Containers get a per-session Docker network (`session-<env>-<session_id>`,
or `session-<session_id>` when the host has no environment set) and
per-SGC data directories mounted into the container; named volumes follow
`manman-sgc[-<env>]-<sgc_id>-<volume>`. Game containers survive host
manager restarts; the host re-attaches on recovery.

### Event Processor → UI (Live Status)

`event-processor` republishes every session status transition it processes
onto a second, dedicated topic exchange, `manmanv2.htmxsse`, independent of
the `manman` and `external` exchanges above. This is a producer-only, purely
additive path: it does not change `status.session.*` routing, payloads, or
the `manman`/`external` exchange bindings.

| Direction | Protocol | Use Case |
|-----------|----------|----------|
| event-processor → manmanv2/ui | RabbitMQ (`manmanv2.htmxsse` topic exchange) | Trigger live htmx/SSE fragment refresh in the UI |

- **Exchange identity**: defined once in `manmanv2/events` (`ExchangeName`,
  `TopicForDeployment`, `DeclareArgs`) — the single source of truth shared by
  both the producer (event-processor) and the consumer (`manmanv2/ui`, via
  `libs/go/htmxsse.DefaultAttachFunc`). Both processes must declare the
  exchange with identical arguments or the UI's attach fails with a 406
  `PRECONDITION_FAILED`.
- **Routing key**: `deployment.<sgcID>` — keyed by ServerGameConfig (SGC), not
  session id, so the UI can subscribe per-deployment regardless of which
  session is currently running under it.
- **Trigger scope**: unfiltered — every transition `event-processor`
  successfully processes (including non-terminal `pending`/`starting`/
  `stopping`) is republished here, unlike the `external`-exchange republish
  which is filtered to terminal states and `running`.
- **Payload**: the same `rmq.SessionStatusUpdate` value published to
  `external`. The payload only needs to be a valid trigger — the UI re-reads
  current state from `control-api` rather than rendering from this payload.
- **Consumer**: `manmanv2/ui` holds its own RabbitMQ connection
  (`RABBITMQ_URL`, dialed once at startup via `initializeSSEHub` in
  `manmanv2/ui/main.go`) dedicated to this exchange — it binds no other
  exchange, and in particular never binds the shared `manman` exchange. The
  connection backs `libs/go/htmxsse.Hub`, mounted at the authenticated SSE
  route `/api/live/deployments`
  (`manmanv2/ui/handlers_sessions_live.go`), which subscribes each connection
  to `deployment.<sgcID>` for exactly the SGCs the requester's
  selected-server scope authorizes (the same scope `handleSessions` renders,
  via a shared resolution helper) and re-derives each pushed row from
  `control-api` at delivery time rather than from the trigger payload. This
  is additive to the UI's existing Postgres use: `PG_DATABASE_URL` remains
  solely for `htmxauth` session storage — `manmanv2/ui` still holds no direct
  Postgres access to domain data (sessions, SGCs, games), and the RabbitMQ
  connection above is used only to trigger fragment refreshes, not to read
  or write domain state. `RABBITMQ_URL` unset, or the broker unreachable, is
  a graceful degradation: the UI still starts and serves
  `/sessions`, and `/api/live/deployments` returns `503` until a broker
  becomes reachable.

### Log Pipeline

Host managers publish session logs to RabbitMQ; log-processor fans them out
to live viewers and, when `PG_DATABASE_URL`+`S3_BUCKET`+`API_ADDRESS` are
set, archives log windows to S3 (with retry of failed window uploads). The
UI's live log viewer and the histogram/historical-log RPCs are served by
log-processor, not control-api.

---

## gRPC Services

Wire contracts live in `protos/`:

- `api.proto` — 68 RPCs; messages split across `api_messages_*.proto`
  (game, gameconfig, server, servergameconfig, session, backup,
  backup_config, action_definition/execution, patch, volume, registration,
  logs, pending restarts).
- `workshop.proto` — 31 RPCs (Workshop search, libraries, installations).
- `log_processor.proto` — log streaming/histogram/historical queries
  (served by log-processor :50053).

gRPC auth is platform-wide `GRPC_AUTH_MODE` (`none`/`oidc` via Keycloak),
set per component but required to match across all of them — see `ENV.md`.

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Execution naming | **Session** | Clear, implies lifecycle and interaction |
| Host deployment | **Docker + socket mount** | Leverages existing release artifact support |
| Game container model | **Direct attach** | Host manages stdin/stdout via Docker attach API; crash detection = stream EOF |
| RabbitMQ topology | **Topic exchange + routing keys** | Simple, proven pattern from v1; additive-only wire contract (LB1) |
| Container identity | **Labels, sole mechanism** | `manman.*` labels are the only recovery source of truth (LB3) |
| Config layering | **ConfigurationStrategy/Patch expansion** | Option B per `docs/DESIGN_SGC_ENV_OVERRIDES.md`; no flat override map (LB4) |
| Live-status UI trigger | **Dedicated `manmanv2.htmxsse` exchange, SGC-keyed** | Keeps htmx/SSE fan-out isolated from `manman`/`external`; SGC key survives session churn |
| UI components | **Reuse `libs/go/htmxui`** | Shared daisyUI-based component library; no manmanv2-local fork (per PRODUCT.md non-goals) |
| Durable restarts | **DB-enforced at-most-one-pending** | `pending_restarts` partial index + atomic claim/expire, not goroutine state |

---

## Directory Structure

```
manmanv2/
├── models/            # Database models (package models), shared by api/processor
├── events/            # RabbitMQ exchange identity (manmanv2.htmxsse) — shared producer/consumer
├── protos/            # Wire contracts: api.proto + api_messages_*.proto, workshop.proto,
│                      #   log_processor.proto, messages.proto
├── api/               # control-api service (gRPC :50051 + REST gateway)
│   ├── handlers/      #   RPC handlers, SessionRestartConsumer, PendingRestartReaper
│   ├── repository/    #   Postgres repositories
│   ├── steam/         #   SteamCMD integration
│   ├── workshop/      #   Workshop manager
│   └── S3_CONFIG.md   #   S3/object storage configuration
├── processor/         # event-processor service (RabbitMQ consumer, status sync)
├── log-processor/     # log-processor service (gRPC :50053, S3 archival)
├── ui/                # manmanv2-ui (Go + templ + HTMX operator UI)
├── host/              # host-manager (bare metal)
│   ├── session/       #   session lifecycle + label-based recovery (manager.go, recovery.go)
│   ├── config/        #   configuration rendering (strategies, patches, env templates)
│   ├── rmq/           #   RabbitMQ publisher/consumer
│   ├── workshop/      #   Workshop addon install orchestration
│   ├── compose/       #   resolver + host-manager compose files
│   ├── DEPLOYMENT.md  #   bare-metal deployment guide
│   └── RESOLVER.md    #   self-updating deployment via compose-resolver
├── migrate/           # control-migration job
│   └── migrations/    #   embedded SQL (001–036), run via libs/go/migrate
├── docs/              # design docs (DESIGN_UI_REDESIGN.md, DESIGN_SGC_ENV_OVERRIDES.md)
│   └── ARCHIVE/       #   self-registration feature docs — reference only
├── testdata/          # integration test fixtures (test game server image)
├── product/           # product brief backing docs (current state, capability map, roadmap)
└── scripts/           # local dev helpers
```

---

## Container Identity & Orphan Recovery

All game containers created by the host manager are labeled at creation time
(`host/session/manager.go`):

- `manman.type` (`game` / `network`)
- `manman.session_id`
- `manman.sgc_id`
- `manman.server_id`

On startup the host manager scans Docker for containers carrying these
labels and re-attaches to running ones (restoring session state) or cleans
up dead ones; periodic cleanup removes containers not tracked by an active
session (`host/session/recovery.go`). These labels are the **sole**
orphan-recovery mechanism — there is no secondary reconciliation path — and
any future multi-tenant/multi-control-plane host-sharing scenario must add
namespacing to them rather than a parallel mechanism (LB3 in
[PRODUCT.md](PRODUCT.md); unknown label keys are ignored by recovery, so
adding new metadata labels is safe).

---

## References

- [PRODUCT.md](PRODUCT.md) — vision, personas, load-bearing decisions (LB1–LB8), roadmap
- [ENV.md](ENV.md) — all environment variables
- [host/RESOLVER.md](host/RESOLVER.md) — self-updating host deployment
- [docs/DESIGN_UI_REDESIGN.md](docs/DESIGN_UI_REDESIGN.md) — UI redesign decisions (draft)
- [docs/DESIGN_SGC_ENV_OVERRIDES.md](docs/DESIGN_SGC_ENV_OVERRIDES.md) — env override layering (Option B accepted)
- `tools/compose-resolver/README.md` — resolver sidecar internals
- Legacy v1 system: `../manman/` (maintenance mode; see `../manman/TOC.md`)
