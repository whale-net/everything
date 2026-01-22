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
│  │  - Routes commands to wrappers via gRPC                              │   │
│  │  - Aggregates health/status for RabbitMQ reporting                   │   │
│  │  - Recovers/reconnects to orphaned wrappers on restart               │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│              │                    │                    │                    │
│         gRPC │               gRPC │               gRPC │                    │
│              │                    │                    │                    │
│  ┌───────────▼──────┐ ┌──────────▼────────┐ ┌────────▼──────────┐         │
│  │  Server Wrapper  │ │  Server Wrapper   │ │  Server Wrapper   │         │
│  │  (Sidecar)       │ │  (Sidecar)        │ │  (Sidecar)        │         │
│  │                  │ │                   │ │                   │         │
│  │  ┌────────────┐  │ │  ┌─────────────┐  │ │  ┌─────────────┐  │         │
│  │  │ Game       │  │ │  │ Game        │  │ │  │ 3rd Party   │  │         │
│  │  │ Server     │  │ │  │ Server      │  │ │  │ Image       │  │         │
│  │  │ Process    │  │ │  │ Process     │  │ │  │             │  │         │
│  │  └────────────┘  │ │  └─────────────┘  │ │  └─────────────┘  │         │
│  └──────────────────┘ └───────────────────┘ └───────────────────┘         │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Deployables

| Deployable | App Type | Deployment | Description |
|------------|----------|------------|-------------|
| `manmanv2-api` | external-api | K8s (Cloud) | User-facing API (gRPC + REST gateway) |
| `manmanv2-processor` | worker | K8s (Cloud) | Event processor, health monitoring |
| `manmanv2-migration` | job | K8s (Cloud) | Database migration runner |
| `manmanv2-host` | worker | Bare metal (Docker) | Host server manager |
| `manmanv2-wrapper` | worker | Bare metal (Docker) | Sidecar for game containers |

### Host Manager

- Docker container with `/var/run/docker.sock` mount
- Uses Docker SDK (Go) for container management
- No privileged mode needed (socket mount sufficient)

### Wrapper (Sidecar)

```
┌─────────────────────────────────────────────────────────┐
│  Docker Network: session-{session_id}                   │
├─────────────────────────────────────────────────────────┤
│  ┌─────────────────────┐  ┌─────────────────────────┐  │
│  │  manmanv2-wrapper   │  │  Game Server Container  │  │
│  │  (gRPC on :50051)   │──│  (e.g., minecraft:latest)│  │
│  └─────────────────────┘  └─────────────────────────┘  │
│                     │                                   │
│        Shared Volume: /data/{session_id}                │
└─────────────────────────────────────────────────────────┘
```

**Key Principle:** Wrappers survive host manager restarts. Host reconnects on recovery.

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

### Host Manager ↔ Wrappers

| Direction | Protocol | Use Case |
|-----------|----------|----------|
| Host → Wrapper | gRPC | Commands, stdin, queries |
| Wrapper → Host | gRPC (streaming) | stdout/stderr, status updates |

**Why gRPC (not RabbitMQ):**
- Lower latency for interactive commands
- Host manages all wrapper connections
- No additional RabbitMQ connections per wrapper
- Host aggregates and batches status to control plane

### Wrapper State Persistence

```
/data/{session_id}/
  ├── wrapper/
  │   ├── state.json      # Current state, container ID
  │   └── grpc.sock       # Unix socket or port info
  ├── game/               # Game server data directory
  └── logs/               # Session logs before upload
```

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

### Wrapper Control (Host → Wrapper)

```protobuf
service WrapperControl {
  rpc Start(...) returns (...);
  rpc Stop(...) returns (...);
  rpc SendInput(...) returns (...);
  rpc GetStatus(...) returns (...);
  rpc StreamOutput(...) returns (stream ...);
}
```

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Execution naming | **Session** | Clear, implies lifecycle and interaction |
| Host deployment | **Docker + socket mount** | Leverages existing release artifact support |
| Wrapper model | **Sidecar container** | Separate container, shares network/volumes |
| RabbitMQ topology | **Topic exchange + routing keys** | Simple, proven pattern from v1 |
| Parameter validation | **Control plane authoritative** | Host/wrapper trust CP, cache locally |

---

## Directory Structure

```
//manman/
├── models.go                    # Database models (package manman)
│                                # Flat structure - no nested pkg/db/
│
├── migrate/                     # Migration tool (manmanv2-migration)
│   ├── main.go                  # CLI runner using libs/go/migrate
│   ├── migrations/              # Embedded SQL migration files
│   │   ├── 001_initial_schema.up.sql
│   │   └── 001_initial_schema.down.sql
│   └── BUILD.bazel              # release_app for migration job
│
├── protos/                      # Protobuf definitions (planned)
│   ├── api.proto                # Control plane API
│   ├── wrapper.proto            # Host ↔ Wrapper protocol
│   └── messages.proto           # Shared message types
│
├── api/                         # manmanv2-api service (planned)
│   ├── main.go
│   ├── handlers/
│   └── BUILD.bazel
│
├── processor/                   # manmanv2-processor service (planned)
│   ├── main.go
│   └── BUILD.bazel
│
├── host/                        # manmanv2-host service (planned)
│   ├── main.go
│   ├── docker/                  # Docker SDK integration
│   ├── grpc/                    # gRPC client for wrappers
│   └── BUILD.bazel
│
├── wrapper/                     # manmanv2-wrapper service (planned)
│   ├── main.go
│   ├── process/                 # Game process management
│   └── BUILD.bazel
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
  - Migration tool configured as release_app: `//manman/migrate:manmanv2-migration`
- [ ] Basic control plane API (CRUD for Game, GameConfig, Server)

### Phase 2: Host Manager
- [ ] Docker SDK integration for container management
- [ ] gRPC client for wrapper communication
- [ ] RabbitMQ integration for control plane communication
- [ ] Session/container lifecycle management

### Phase 3: Wrapper
- [ ] gRPC server implementation
- [ ] Game container management (spawn, monitor, attach)
- [ ] State persistence for recovery
- [ ] stdin/stdout/stderr forwarding

### Phase 4: Integration
- [ ] End-to-end flow: deploy → start session → interact → stop
- [ ] Health monitoring and status aggregation
- [ ] Port allocation enforcement

### Phase 5: Polish
- [ ] Logging pipeline to S3
- [ ] Backup/restore for game saves
- [ ] Parameter system refinement
- [ ] 3rd party image support

---

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
