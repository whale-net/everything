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
