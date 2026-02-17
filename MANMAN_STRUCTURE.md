# ManMan Repository Structure

This document explains the organization of the ManMan codebase and where to find things.

## Overview

The repository contains two versions of ManMan:

- **ManMan V1** (`//manman`) - Legacy Python services, maintenance mode
- **ManMan V2** (`//manmanv2`) - Current Go services, actively developed

**Start with V2 for new development.** V1 is kept for backward compatibility.

## Directory Layout

```
whale_net/everything/
├── manman/                  ← V1 Services (Legacy, Python)
│   ├── README.md           ← V1 overview and quick start
│   ├── GETTING_STARTED.md  ← Entry point (redirects to V2)
│   ├── src/                ← V1 Python source code
│   │   ├── host/           ← FastAPI services
│   │   ├── worker/         ← Background workers
│   │   ├── repository/     ← Data access layer
│   │   └── migrations/     ← Database migrations
│   ├── management-ui/      ← Go web interface
│   ├── docs/               ← V1 Feature documentation
│   │   ├── README.md
│   │   ├── PRODUCTION_DEPLOYMENT.md
│   │   ├── PARAMETER_SYSTEM.md
│   │   ├── BACKUP_SYSTEM.md
│   │   └── THIRD_PARTY_IMAGES.md
│   ├── design/             ← Reference diagrams and schemas
│   │   ├── ARCHIVE/        ← Old implementation notes (archived)
│   │   ├── *.svg           ← Architecture diagrams
│   │   └── *.sql           ← Database schemas
│   ├── clients/            ← Client libraries
│   ├── test_data/          ← Test fixtures
│   └── BUILD.bazel         ← V1 Helm chart definition
│
├── manmanv2/               ← V2 Services (Current, Go)
│   ├── README.md           ← Local development setup
│   ├── QUICK-START.md      ← 5-minute getting started
│   ├── ARCHITECTURE.md     ← System design and components
│   ├── ABOUT.md            ← Tiltfile design decisions
│   ├── api/                ← Control plane gRPC API
│   │   ├── handlers/
│   │   ├── repository/
│   │   ├── main.go
│   │   └── S3_CONFIG.md
│   ├── processor/          ← Event processor service
│   │   ├── README.md
│   │   ├── VERIFICATION.md
│   │   └── handlers/
│   ├── host/               ← Bare metal host manager
│   │   ├── DEPLOYMENT.md   ← Host deployment guide
│   │   └── session/
│   ├── log-processor/      ← Log aggregation service
│   ├── migrate/            ← Database migration runner
│   ├── ui/                 ← Web interface
│   │   ├── README.md
│   │   ├── handlers/
│   │   └── templates/
│   ├── examples/           ← Example implementations
│   │   └── external-subscriber/  ← Event subscriber example
│   ├── protos/             ← Protocol buffer definitions
│   ├── testdata/           ← Integration test fixtures
│   ├── scripts/            ← Helper scripts
│   ├── docs/               ← Implementation notes (archived)
│   │   └── ARCHIVE/
│   ├── Tiltfile            ← Local development orchestration
│   ├── models.go           ← Shared data models
│   └── BUILD.bazel         ← V2 Helm chart definition
```

## What Lives Where

### Control Plane Services (API, Processor, etc.)

**Location:** `//manmanv2`

- **API Server** (`//manmanv2/api`) - gRPC control plane API
- **Event Processor** (`//manmanv2/processor`) - Processes events, syncs to database
- **Migration Service** (`//manmanv2/migrate`) - Database schema management
- **Log Processor** (`//manmanv2/log-processor`) - Aggregates container logs
- **Web UI** (`//manmanv2/ui`) - Management interface

**Build Helm chart:**
```bash
bazel build //manmanv2:manmanv2_chart
```

**Run locally with Tilt:**
```bash
cd manmanv2
tilt up
```

### Execution Plane (Host Manager)

**Location:** `//manmanv2/host`

The host manager runs on bare metal servers and:
- Communicates with control plane via RabbitMQ
- Manages Docker containers for game servers
- Handles session orchestration
- Reports status and events

**Build binary:**
```bash
bazel build //manmanv2/host:host
```

**Deployment guide:** `//manmanv2/host/DEPLOYMENT.md`

### Database & Models

**Location:** `//manmanv2`

- **Data Models** (`models.go`) - Shared Go structures
- **Migrations** (`//manmanv2/migrate`) - Alembic-based schema management
- **Repositories** (in each service) - Data access patterns

### Protocol Definitions

**Location:** `//manmanv2/protos`

- **api.proto** - Control plane gRPC service definition
- Generated clients and servers in `//generated/go/`

### Legacy V1 Services (Maintenance Only)

**Location:** `//manman/src`

V1 services that are no longer developed but still used:
- **Experience API** - User-facing game server API
- **Status API** - Internal status monitoring
- **Worker DAL API** - Worker data access layer
- **Status Processor** - Background event processor

See `//manman/README.md` for V1 details.

## Documentation

### Getting Started
- **New user?** → Start at `//manmanv2/README.md`
- **Quick start (5 min)?** → `//manmanv2/QUICK-START.md`

### Architecture & Design
- **System design?** → `//manmanv2/ARCHITECTURE.md`
- **How Tilt works?** → `//manmanv2/ABOUT.md`
- **Deployment decisions?** → Check file READMEs

### Feature Documentation
- **Parameters?** → `//manman/docs/PARAMETER_SYSTEM.md`
- **Backups?** → `//manman/docs/BACKUP_SYSTEM.md`
- **Custom images?** → `//manman/docs/THIRD_PARTY_IMAGES.md`
- **Production deployment?** → `//manman/docs/PRODUCTION_DEPLOYMENT.md`

### Service-Specific Docs
- **API Service** → `//manmanv2/api/S3_CONFIG.md`
- **Event Processor** → `//manmanv2/processor/README.md`
- **Host Manager** → `//manmanv2/host/DEPLOYMENT.md`
- **External Events** → `//manmanv2/examples/external-subscriber/README.md`

## Common Tasks

### Local Development

```bash
cd manmanv2
tilt up                      # Start control plane
cd ../                        # In another terminal
bazel run //manmanv2/host:host  # Start host manager (optional)
```

### Building Services

```bash
# Build V2 control plane
bazel build //manmanv2/...

# Build V2 Helm chart
bazel build //manmanv2:manmanv2_chart

# Build V1 services (legacy)
bazel build //manman/...
bazel build //manman:manman_chart
```

### Testing

```bash
# Run all tests
bazel test //manmanv2/...

# Run specific service tests
bazel test //manmanv2/processor:...
bazel test //manmanv2/api:...
```

### Deployment

```bash
# Build both V1 and V2 charts
bazel build //manman:manman_chart    # V1
bazel build //manmanv2:manmanv2_chart # V2

# Deploy with Helm
helm install manman-v2 \
  bazel-bin/manmanv2/control-services_chart/control-services \
  --namespace manmanv2
```

## Understanding Component Connections

### V2 Architecture (Current)

```
┌─────────────────────────────────────────┐
│  CONTROL PLANE (K8s)                    │
│                                         │
│ ┌────────────┐  ┌──────────────────┐  │
│ │ API Server │  │ Event Processor  │  │
│ │ (gRPC)     │  │ (RabbitMQ)       │  │
│ └────────┬───┘  └────────┬─────────┘  │
│          │               │             │
│      ┌───▼───────────────▼───┐         │
│      │   PostgreSQL Database │         │
│      └───┬──────────────┬────┘         │
│          │              │              │
│    ┌─────▼──┐      ┌────▼─────┐       │
│    │ RabbitMQ         Migration│       │
│    │ Messages         Job      │       │
│    └─────┬──┘        └────────┘       │
└─────────┼─────────────────────────────┘
          │
          │ (Command/Status/Events)
          │
┌─────────▼─────────────────────────────┐
│  EXECUTION PLANE (Bare Metal)         │
│                                       │
│ ┌──────────────────────────────────┐  │
│ │ Host Manager                     │  │
│ │ (RabbitMQ Consumer)              │  │
│ │ ┌────────────────────────────┐   │  │
│ │ │ Game Containers            │   │  │
│ │ │ (Minecraft, Valheim, etc.) │   │  │
│ │ └────────────────────────────┘   │  │
│ └──────────────────────────────────┘  │
└───────────────────────────────────────┘
```

### Key Connections

1. **API → Database** - CRUD operations on game configs, sessions
2. **API → RabbitMQ** - Sends commands and queries to hosts
3. **Host Manager → RabbitMQ** - Consumes commands, publishes events
4. **Processor → RabbitMQ** - Consumes events from hosts
5. **Processor → Database** - Updates status based on events
6. **UI → API** - gRPC calls for management operations

## Finding Specific Features

### Parameters & Configuration
- **Design** → `//manmanv2/api/handlers/` and `/configure` handler
- **Doc** → `//manman/docs/PARAMETER_SYSTEM.md`
- **Test data** → `//manmanv2/testdata/`

### Backups & S3
- **Implementation** → `//manmanv2/api/` (backup handlers)
- **Config** → `//manmanv2/api/S3_CONFIG.md`
- **Doc** → `//manman/docs/BACKUP_SYSTEM.md`

### Event Processing
- **Implementation** → `//manmanv2/processor/handlers/`
- **Example** → `//manmanv2/examples/external-subscriber/`
- **Doc** → `//manmanv2/processor/README.md`

### Container Management
- **Implementation** → `//manmanv2/host/session/`
- **Docker SDK** → `//manmanv2/host/docker.go`
- **Deployment** → `//manmanv2/host/DEPLOYMENT.md`

### Logging
- **Implementation** → `//manmanv2/log-processor`
- **Architecture** → `//manmanv2/ARCHITECTURE.md` (Logging section)

## Archived Documentation

Historical implementation notes and RFC documents are preserved in:
- `//manman/design/ARCHIVE/` - V1 design notes
- `//manman/docs/ARCHIVE/` - V1 phase completion reports
- `//manmanv2/docs/ARCHIVE/` - V2 implementation reports

These are useful for understanding how decisions were made but should not be used as current documentation.

See the respective ARCHIVE/README.md files for details.
