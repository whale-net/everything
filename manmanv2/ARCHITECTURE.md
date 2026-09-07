# ManMan V2 - System Design Document

> **Status:** Design Complete | **Language:** Go | **Pattern:** Docker-out-of-Docker

## Overview

ManManV2 is a game server management platform with a split-plane architecture:

| Plane | Location | Responsibility |
|-------|----------|----------------|
| **Control Plane** | Cloud (K8s) | Orchestration, data storage, user-facing APIs |
| **Execution Plane** | Bare Metal | Host managers and game server containers |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            CONTROL PLANE (Cloud)                             │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐                  │
│  │  Public API  │    │  Admin API   │    │  Event       │                  │
│  │  (gRPC/REST) │    │  (gRPC/REST) │    │  Processor   │                  │
│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘                  │
│         │                   │                   │                          │
│         └─────────┬─────────┴─────────┬─────────┘                          │
│                   │                   │                                     │
│         ┌─────────▼─────────┐   ┌─────▼─────────┐                          │
│         │    PostgreSQL     │   │   RabbitMQ    │                          │
│         │    (databass)     │   │   (events)    │                          │
│         └───────────────────┘   └───────┬───────┘                          │
│                                         │                                   │
│                               ┌─────────▼─────────┐                        │
│                               │       S3          │                        │
│                               │ (logs/backups)    │                        │
│                               └───────────────────┘                        │
└─────────────────────────────────────────────────────────────────────────────┘
                                        │
                                   RabbitMQ
                                   (commands/status)
                                        │
┌───────────────────────────────────────▼─────────────────────────────────────┐
│                         EXECUTION PLANE (Bare Metal)                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      Host Server Manager                             │   │
│  │  - Manages Docker containers via Docker SDK                          │   │
│  │  - Stdin forwarding via Docker attach                                │   │
│  │  - Aggregates health/status for RabbitMQ reporting                   │   │
│  │  - Recovers/re-attaches to game containers on restart                │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│              │                    │                    │                    │
│      attach  │            attach  │            attach  │                    │
│              │                    │                    │                    │
│  ┌───────────▼──────┐ ┌──────────▼────────┐ ┌────────▼──────────┐         │
│  │  Game Server     │ │  Game Server      │ │  Game Server      │         │
│  │  Container       │ │  Container        │ │  Container        │         │
│  │                  │ │                   │ │                   │         │
│  │  (e.g. game img) │ │  (e.g. game img)  │ │  (3rd Party Img)  │         │
│  └──────────────────┘ └───────────────────┘ └───────────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Deployables

| Deployable | App Type | Deployment | Description |
|------------|----------|------------|-------------|
| `control-api` | external-api | K8s (Cloud) | User-facing API (gRPC + REST gateway) |
| `event-processor` | worker | K8s (Cloud) | Event processor, health monitoring |
| `control-migration` | job | K8s (Cloud) | Database migration runner |
| `manmanv2-host` | worker | Bare metal (Docker) | Host server manager |

### Host Manager

- Docker container with `/var/run/docker.sock` mount
- Uses Docker SDK (Go) for container management
- No privileged mode needed (socket mount sufficient)

### Game Containers

```
┌─────────────────────────────────────────────────────────┐
│  Docker Network: session-{session_id}                   │
├─────────────────────────────────────────────────────────┤
│  ┌─────────────────────────────────────────────┐       │
│  │  Game Server Container                      │       │
│  │  (e.g., minecraft:latest)                   │       │
│  │  stdin/stdout via Docker attach             │       │
│  └─────────────────────────────────────────────┘       │
│                     │                                   │
│          Volume: /data/gsc-{env}-{sgc_id}:/data/game   │
└─────────────────────────────────────────────────────────┘
```

**Key Principle:** Game containers survive host manager restarts. Host re-attaches on recovery.

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
│ last_seen   │         │ metadata    │
└──────┬──────┘         └──────┬──────┘
       │                       │
       │ has many              │ has many
       ▼                       ▼
┌──────────────────┐    ┌──────────────┐
│ ServerGameConfig │◄───│  GameConfig  │
├──────────────────┤    ├──────────────┤
│ sgc_id           │    │ config_id    │
│ server_id (FK)   │    │ game_id (FK) │
│ game_config_id   │    │ name         │
│ port_bindings    │    │ image        │
│ parameters       │    │ args_template│
│ status           │    │ env_template │
└────────┬─────────┘    │ files        │
         │              │ parameters   │
         │ has many     └──────────────┘
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

### Parameter System

Parameters can be overridden at three levels:

| Level | Example | Use Case |
|-------|---------|----------|
| **GameConfig** | `max_players=20` | Base defaults |
| **ServerGameConfig** | `port=25565` | Server-specific settings |
| **Session** | `world_name=test` | Per-execution overrides |

### Port Management

```
┌───────────────────┐
│  ServerPort       │
├───────────────────┤
│ server_id (FK)    │
│ port              │
│ protocol (TCP/UDP)│
│ sgc_id (FK)       │
│ allocated_at      │
└───────────────────┘
```

**Constraints:**
- Port allocated to one ServerGameConfig at a time
- Multiple Sessions can use port sequentially (not concurrently)
- API enforces allocation; eventual consistency acceptable

### Pending Restarts (durable restart, Track B)

```
┌───────────────────────────┐
│  PendingRestart           │
├───────────────────────────┤
│ pending_restart_id        │
│ server_game_config_id (FK)│
│ gating_session_id (FK)    │
│ status                    │
│ stall_deadline            │
│ started_session_id        │
│ failure_reason            │
│ created_at                │
│ resolved_at               │
└───────────────────────────┘
```

`pending_restarts` (migration `036_pending_restarts`) gives "a Start is
pending for this deployment, gated on session `<id>`'s Stop reaching a
terminal status" a durable, control-plane-local home, instead of living only
on `finishRestartInBackground`'s goroutine stack
(`manmanv2/ui/handlers_deployment_actions.go`). It is control-plane state
only — restarting does not add a new wire routing key or payload field.

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
`status.session.*` semantics. `manmanv2/ui`'s `restartDeployment` now
dispatches a single `RestartDeployment` RPC and holds no restart state of
its own (#1733) — the goroutine-based stop-then-start
(`restartDeployment`/`finishRestartInBackground`) described above is gone.

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
duplicating that logic or adding a new cross-service RPC, both of which the
consumer is explicitly scoped to avoid (see #1731). It reuses the same
`SessionHandler` instance as the gRPC API (via a narrow `DeferredStarter`
interface, `StartSession(ctx, *pb.StartSessionRequest) (*pb.StartSessionResponse, error)`),
so it shares that handler's `CommandPublisher`/`workshop.Manager` rather than
constructing a second one.

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
half: a plain ticker (modelled on
`SessionStatusHandler.StartStaleSessionChecker`,
`manmanv2/processor/handlers/session_status.go`), not another
`status.session.#` consumer.

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
above. For every row it expires, the reaper logs one WARNING (`server_game_config_id`,
`gating_session_id`, `pending_restart_id`, `created_at`, `stall_deadline`) —
WARNING per `AGENTS.md` § Logging Levels, since the system kept going but the
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
defaults), order-of-magnitude comparable to the ~15s bound
`waitForNoLiveSession` used to give the client-side poll this table replaces
(NFR12). The interval must stay well below the timeout or the bound is
meaningless — enforced only by review, not code.

**Not SCD2:** this table intentionally does not use `valid_from`/`valid_to`
(see `AGENTS.md` § SCD2). A pending restart is a short-lived work intent with
its own terminal state machine (`status` + `resolved_at`), not dimension
history — SCD2's "current value = row with `valid_to IS NULL`" model doesn't
fit a record that is created once and resolved exactly once.

**Read half — `ListPendingRestarts` RPC (`manmanv2/api/handlers/session.go`,
control-api, #1735):** the operator-visibility counterpart to the three
mechanisms above. FR12 requires that moving orchestration server-side not
make a post-dispatch failure *less* visible than the old client-side
goroutine's inline error was, so `manmanv2/ui`'s `/sessions` deployment row
reads this table back (via `PendingRestartRepository.GetLatestBySGCIDs`) and
renders a badge distinguishing `pending` (in progress) from `failed`/
`expired` (stalled/failed) — see `manmanv2/ui/README.md` § "Restart State
Visibility" for the badge mapping and the UI-side read path. One batched RPC
per page render (every rendered SGC id in one call), never one per row.
`GetLatestBySGCIDs` excludes a resolved record older than
`pendingRestartVisibilityWindow` (5 minutes, `manmanv2/api/repository/postgres/pending_restart.go`)
so a stale terminal badge doesn't outlive its usefulness. Strictly read-only:
this RPC never writes to `pending_restarts`.

---

## Communication Patterns

### Control Plane ↔ Host Manager

| Direction | Protocol | Use Case |
|-----------|----------|----------|
| CP → Host | RabbitMQ | Commands (start, stop, configure) |
| Host → CP | RabbitMQ | Status updates, health, events |

**Message Types (Topic Exchange):**
- `command.*` - Control commands
- `status.host.*` - Host-level status
- `status.session.*` - Session-level status
- `health.*` - Health/keepalive

### Host Manager ↔ Game Containers

| Direction | Mechanism | Use Case |
|-----------|-----------|----------|
| Host → Game | Docker attach (stdin) | Send commands to game |
| Game → Host | Docker attach (stdout/stderr) | Stream game output |

The host attaches to each game container via the Docker API. Stdin is written
directly to the container's attached connection. Stdout/stderr are demuxed from
the same stream using Docker's 8-byte multiplexed header format.

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
- **Consumer (issue #1724)**: `manmanv2/ui` holds its own RabbitMQ connection
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
  a graceful degradation (NFR3/NFR8): the UI still starts and serves
  `/sessions`, and `/api/live/deployments` returns `503` until a broker
  becomes reachable.

---

## gRPC Service Definitions

### Control Plane API

```protobuf
service ManManAPI {
  // Server management
  rpc ListServers(...) returns (...);
  rpc GetServer(...) returns (...);

  // Game/Config management
  rpc ListGames(...) returns (...);
  rpc CreateGameConfig(...) returns (...);

  // Deployment
  rpc DeployGameConfig(...) returns (...);  // Creates ServerGameConfig
  rpc StartSession(...) returns (...);
  rpc StopSession(...) returns (...);
  rpc SendInput(...) returns (...);         // stdin to running game

  // Status
  rpc GetSessionStatus(...) returns (...);
  rpc StreamSessionLogs(...) returns (stream ...);
}
```

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Execution naming | **Session** | Clear, implies lifecycle and interaction |
| Host deployment | **Docker + socket mount** | Leverages existing release artifact support |
| Game container model | **Direct attach** | Host manages stdin/stdout via Docker attach API |
| RabbitMQ topology | **Topic exchange + routing keys** | Simple, proven pattern from v1 |
| Live-status UI trigger | **Dedicated `manmanv2.htmxsse` exchange, SGC-keyed** | Keeps htmx/SSE fan-out isolated from `manman`/`external`; SGC key survives session churn |
| Parameter validation | **Control plane authoritative** | Host trusts CP, caches locally |

---

## Directory Structure

```
//manman/
├── models.go                    # Database models (package manman)
│                                # Flat structure - no nested pkg/db/
│
├── migrate/                     # Migration tool (control-migration)
│   ├── main.go                  # CLI runner using libs/go/migrate
│   ├── migrations/              # Embedded SQL migration files
│   │   ├── 001_initial_schema.up.sql
│   │   └── 001_initial_schema.down.sql
│   └── BUILD.bazel              # release_app for migration job
│
├── protos/                      # Protobuf definitions (planned)
│   ├── api.proto                # Control plane API
│   └── messages.proto           # Shared message types
│
├── api/                         # control-api service (planned)
│   ├── main.go
│   ├── handlers/
│   └── BUILD.bazel
│
├── processor/                   # event-processor service (planned)
│   ├── main.go
│   └── BUILD.bazel
│
├── host/                        # manmanv2-host service (planned)
│   ├── main.go
│   ├── session/                 # Session lifecycle management
│   ├── rmq/                     # RabbitMQ consumer/publisher
│   └── BUILD.bazel
│
├── testdata/                    # Integration test fixtures
│   ├── Dockerfile               # Test game server image
│   └── test_game_server.sh      # Simulated game server
│
└── BUILD.bazel                  # Root BUILD with :models target

# Existing v1 code (to be deprecated)
├── src/                         # [LEGACY] Python v1 code
├── management-ui/               # [LEGACY] Go management UI
└── clients/                     # [LEGACY] Generated clients
```

**Design Principles:**
- Flat package structure (avoid deep nesting)
- Shared models at root level (package `manman`)
- Each service is a separate subdirectory with its own main.go
- Migration tool uses go:embed for SQL files

### New Shared Infrastructure

```
//libs/go/
├── migrate/                     # Generic database migration library ✓
│   ├── migrate.go               # Runner type with Up/Down/Steps/Version/Force
│   ├── cli.go                   # RunCLI helper for CLI applications
│   └── BUILD.bazel
├── grpc/                        # Shared gRPC utilities (planned)
└── rmq/                         # RabbitMQ utilities (planned)

//tools/bazel/
└── grpc.bzl                     # gRPC build rules ✓
```

---

## Implementation Phases

### Phase 1: Foundation
- [x] **Protobuf definitions and gRPC build infrastructure** ✓
  - Added `rules_proto` to MODULE.bazel
  - Added gRPC and protobuf Go dependencies
  - Created `//tools/bazel/grpc.bzl` with `go_grpc_library` macro
  - Demo app validated: `//demo/hello_grpc_go/`
- [x] **Core data models and database schema** ✓
  - Created `//manman/models.go` with all database models (package manman)
  - Created SQL migrations in `//manman/migrate/migrations/`
  - Built generic migration library at `//libs/go/migrate/`
  - Migration tool configured as release_app: `//manman/migrate:control-migration`
- [x] **Basic control plane API (CRUD for Game, GameConfig, Server)** ✓
  - Full CRUD operations for all entities
  - gRPC API with REST gateway
  - Validation and error handling

### Phase 2: Host Manager
- [x] Docker SDK integration for container management ✓
- [x] RabbitMQ integration for control plane communication ✓
- [x] Session/container lifecycle management ✓

### Phase 3: Game Container Direct Management
- [x] Game container creation with OpenStdin ✓
- [x] Stdin forwarding via Docker attach ✓
- [x] Stdout/stderr demux via multiplexed stream ✓
- [x] Crash detection on stream EOF ✓

### Phase 4: Integration
- [x] **End-to-end flow: deploy → start session → interact → stop** ✓
  - Complete session lifecycle via RabbitMQ commands
  - Host manager orchestration
  - Direct game container management
- [x] **Health monitoring and status aggregation** ✓
  - Event processor service (Phase 6)
  - Real-time database synchronization
  - Stale host detection
- [x] **Orphan container detection and cleanup** ✓
  - Label-based reconciliation
  - Recovery on host manager restart
  - Implemented in `manman/host/session/recovery.go`
- [x] **Port allocation enforcement** ✓
  - ServerPortRepository with full CRUD operations
  - Atomic batch allocation with transaction support
  - API integration in DeployGameConfig and DeleteServerGameConfig
  - Comprehensive test suite (15 tests, 100% pass rate)
  - Database migration 008_server_ports

### Phase 5: Polish
- [x] **Logging pipeline to S3** ✓
  - Cloud-agnostic S3 library (AWS, OVH, DigitalOcean, MinIO)
  - Session log upload to S3
  - Custom endpoint support
- [x] **Backup/restore for game saves** ✓
  - Database schema with backups table
  - Complete API layer for backup operations
  - S3 integration for storage
- [x] **Parameter system refinement** ✓
  - Parameter utilities library (`libs/go/params/`)
  - Type-safe validation
  - Template rendering
- [x] **3rd party image support** ✓
  - Entrypoint and command fields
  - Support for official Docker Hub images
  - Documentation for popular games

### Phase 6: Event Processing & Observability
- [x] **Event Processor Service** ✓
  - RabbitMQ consumer for internal events
  - Database synchronization
  - External event publishing for cross-domain integration
  - Session state machine validation
  - Stale host detection (10s threshold)
- [x] **Testing & Validation** ✓
  - Unit tests for handlers and state machine
  - Integration tests for end-to-end flows
  - Mock repositories for testing
  - Comprehensive port allocation tests (15 tests)
- [x] **External Integration** ✓
  - Reference subscriber implementation
  - Examples for Slack, Prometheus, audit logging
  - Documentation and extension patterns
- [x] **Port Allocation Enforcement** ✓
  - Test-driven development approach
  - 15 comprehensive tests covering all edge cases
  - Full PostgreSQL implementation
  - API integration with rollback on failure

---

## Orphan Prevention Strategy (Phase 4)

### Problem

With game containers running independently from the host manager process, orphaned resources can occur:

1. **Host manager crash**: Loses in-memory session state, can't track running containers
2. **Network failures**: Host can't reach containers, but games still run
3. **Deployment issues**: Old containers from previous deployments left behind

### Solution: Label-Based Reconciliation

**1. Container Labeling**

All ManMan-created resources MUST be labeled:

```go
// Game container labels (created directly by host)
labels := map[string]string{
    "manman.type":        "game",
    "manman.session_id":  "12345",
    "manman.sgc_id":      "67890",
    "manman.server_id":   "42",
    "manman.created_at":  "2026-01-29T12:00:00Z",
}

// Network labels
labels := map[string]string{
    "manman.type":        "network",
    "manman.session_id":  "12345",
    "manman.server_id":   "42",
}
```

**2. Host Manager Startup Reconciliation**

On startup, host manager scans Docker for ManMan game containers:

```go
func (h *HostManager) ReconcileOnStartup(ctx context.Context) error {
    // 1. Find all game containers with manman.type=game
    games := h.docker.ListContainers(ctx, map[string]string{
        "manman.type": "game",
    })

    // 2. For each game container, attempt to re-attach or clean up
    for _, game := range games {
        sessionID := game.Labels["manman.session_id"]

        if game.Running {
            // Re-attach for stdin/stdout
            attachResp := h.docker.AttachToContainer(ctx, game.ID)
            h.restoreSession(sessionID, game.ID, attachResp)
        } else {
            // Dead container — remove it
            h.docker.RemoveContainer(ctx, game.ID, true)
        }
    }

    // 3. Clean up orphaned networks
    h.cleanupOrphanedNetworks(ctx)
}
```

**3. Periodic Orphan Cleanup**

Background goroutine runs every 5 minutes:

```go
func (h *HostManager) OrphanCleanupLoop(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    for {
        select {
        case <-ticker.C:
            h.cleanupOrphans(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (h *HostManager) cleanupOrphans(ctx context.Context) {
    // Find game containers not in active session list
    activeSGCIDs := h.getActiveSGCIDs()

    games := h.docker.ListContainers(ctx, map[string]string{
        "manman.type": "game",
    })

    for _, game := range games {
        sgcID := game.Labels["manman.sgc_id"]

        // Not tracked by this host manager?
        if !activeSGCIDs.Contains(sgcID) {
            age := time.Since(game.CreatedAt)

            // Grace period: 5 minutes (in case host manager just started)
            if age > 5*time.Minute {
                log.Printf("Orphaned game container %s (sgc_id %s), cleaning up",
                    game.ID, sgcID)
                h.docker.StopContainer(ctx, game.ID, true)
                h.docker.RemoveContainer(ctx, game.ID, true)
            }
        }
    }
}
```

**4. TTL-Based Cleanup (Future Enhancement)**

Add TTL labels for additional safety:

```go
labels["manman.ttl"] = "24h"  // Absolute max lifetime
labels["manman.heartbeat"] = time.Now().Format(time.RFC3339)
```

Containers without recent heartbeat updates get cleaned up even if host manager is down.

### Benefits

- **Self-healing**: Host manager restart automatically discovers and re-attaches to surviving game containers
- **Cleanup on failure**: Orphaned containers are detected and terminated
- **Multi-host safe**: Each host only manages containers with matching `manman.server_id`
- **Audit trail**: Labels provide metadata for debugging ("why is this container running?")

## Deferred Decisions

Items to address during implementation:

1. **Offline host handling** - Message TTL, dead letter queues
2. **Session log persistence** - Real-time vs batch, retention policy

---

## References

- Existing v1 implementation: `//manman/src/`
- Release app patterns: `//tools/bazel/release.bzl`
- RabbitMQ library: `//libs/python/rmq/`
- PostgreSQL patterns: `//libs/python/postgres/`
