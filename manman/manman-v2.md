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
- [x] **Basic control plane API (CRUD for Game, GameConfig, Server)** ✓
  - Full CRUD operations for all entities
  - gRPC API with REST gateway
  - Validation and error handling

### Phase 2: Host Manager
- [x] Docker SDK integration for container management ✓
- [x] gRPC client for wrapper communication ✓
- [x] RabbitMQ integration for control plane communication ✓
- [x] Session/container lifecycle management ✓

### Phase 3: Wrapper
- [x] gRPC server implementation ✓
- [x] Game container management (spawn, monitor, attach) ✓
- [x] State persistence for recovery ✓
- [x] stdin/stdout/stderr forwarding ✓

### Phase 4: Integration
- [x] **End-to-end flow: deploy → start session → interact → stop** ✓
  - Complete session lifecycle via RabbitMQ commands
  - Host manager orchestration
  - Wrapper sidecar integration
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

With wrapper and game containers running separately from the host manager process, orphaned resources can occur:

1. **Host manager crash**: Loses in-memory session state, can't track running containers
2. **Wrapper crash without recovery**: Game container keeps running, no wrapper to manage it
3. **Network failures**: Host can't reach wrapper, but both wrapper and game still run
4. **Deployment issues**: Old containers from previous deployments left behind

### Solution: Label-Based Reconciliation

**1. Container Labeling**

All ManMan-created resources MUST be labeled:

```go
// Wrapper container labels
labels := map[string]string{
    "manman.type":        "wrapper",
    "manman.session_id":  "12345",
    "manman.sgc_id":      "67890",
    "manman.server_id":   "42",
    "manman.created_at":  "2026-01-29T12:00:00Z",
}

// Game container labels (created by wrapper)
labels := map[string]string{
    "manman.type":        "game",
    "manman.session_id":  "12345",
    "manman.sgc_id":      "67890",
    "manman.wrapper_id":  "abc123",  // Links back to wrapper
}

// Network labels
labels := map[string]string{
    "manman.type":        "network",
    "manman.session_id":  "12345",
    "manman.server_id":   "42",
}
```

**2. Host Manager Startup Reconciliation**

On startup, host manager scans Docker for ManMan containers:

```go
func (h *HostManager) ReconcileOnStartup(ctx context.Context) error {
    // 1. Find all ManMan containers
    containers := h.docker.ListContainers(ctx, map[string]string{
        "manman.server_id": h.serverID,
    })

    // 2. For each wrapper, attempt to reconnect
    for _, wrapper := range wrappers {
        sessionID := wrapper.Labels["manman.session_id"]

        // Try gRPC connection
        if client := tryConnect(wrapper); client != nil {
            // Wrapper is alive! Restore session state
            status := client.GetStatus(sessionID)
            h.restoreSession(sessionID, wrapper.ID, status)
        } else {
            // Wrapper is dead, check if game is still running
            gameContainer := findGameContainer(sessionID)
            if gameContainer != nil && gameContainer.Running {
                // Orphaned game! Clean it up
                log.Printf("Orphaned game container %s, terminating", gameContainer.ID)
                h.docker.StopContainer(ctx, gameContainer.ID, true)
            }
            // Remove dead wrapper
            h.docker.RemoveContainer(ctx, wrapper.ID, true)
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
    // Find containers not in active session list
    activeSessionIDs := h.getActiveSessionIDs()

    allContainers := h.docker.ListContainers(ctx, map[string]string{
        "manman.server_id": h.serverID,
    })

    for _, container := range allContainers {
        sessionID := container.Labels["manman.session_id"]

        // Not tracked by this host manager?
        if !activeSessionIDs.Contains(sessionID) {
            age := time.Since(container.CreatedAt)

            // Grace period: 5 minutes (in case host manager just started)
            if age > 5*time.Minute {
                log.Printf("Orphaned container %s (session %s), cleaning up",
                    container.ID, sessionID)
                h.docker.StopContainer(ctx, container.ID, true)
                h.docker.RemoveContainer(ctx, container.ID, true)
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

- **Self-healing**: Host manager restart automatically discovers and reconnects to surviving wrappers
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
