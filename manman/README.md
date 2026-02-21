# ManMan V1 - Legacy Manifest Management System

**⚠️ Status: Legacy/Maintenance Mode**

This directory contains the original ManMan V1 services written in Python. These services remain in use but are in maintenance mode. **New development should target [ManManV2](../manmanv2)** instead.

## Quick Navigation

- **🚀 Starting fresh?** → Go to [ManManV2 (../manmanv2)](../manmanv2)
- **📖 Need documentation?** → See [docs/README.md](./docs/README.md)
- **🛠️ Local development?** → See [../manmanv2/README.md](../manmanv2/README.md)
- **📦 Deploying V1 services?** → See [docs/PRODUCTION_DEPLOYMENT.md](./docs/PRODUCTION_DEPLOYMENT.md)

## What's Here

This directory contains the **legacy V1 services** (Python-based):

- **Experience API** - User-facing game server management API
- **Status API** - Internal status monitoring and health checks
- **Worker DAL API** - Worker data access layer
- **Status Processor** - Background event processor
- **Worker** - General background task processor
- **Migration** - Database schema migration runner
- **Management UI** - Admin web interface (Go-based wrapper)

## Directory Structure

```
manman/
├── README.md                    ← You are here (legacy service overview)
├── GETTING_STARTED.md          ← Entry point guide (redirects to V2)
├── docs/                       ← Feature documentation and guides
│   ├── README.md              ← Documentation index
│   ├── PRODUCTION_DEPLOYMENT.md ← Deployment configuration
│   ├── PARAMETER_SYSTEM.md    ← Parameter configuration
│   ├── BACKUP_SYSTEM.md       ← Backup & restore
│   └── THIRD_PARTY_IMAGES.md  ← Custom Docker images
├── src/                        ← V1 Python source code
│   ├── host/                  ← FastAPI services
│   ├── worker/                ← Worker service
│   ├── repository/            ← Data access layer
│   ├── migrations/            ← Database migrations
│   └── models.py              ← Data models
├── management-ui/             ← Go-based web interface
├── clients/                   ← Client libraries
└── test_data/                 ← Test fixtures
```

## Building & Running

### Build V1 Services

```bash
# Build all V1 services
bazel build //manman/...

# Build Helm chart
bazel build //manman:manman_chart
```

### Run Services Locally

```bash
# Experience API
bazel run //manman/src/host:experience_api

# Status API
bazel run //manman/src/host:status_api

# Worker DAL API
bazel run //manman/src/host:worker_dal_api

# Status Processor
bazel run //manman/src/host:status_processor
```

### Deploy to Kubernetes

```bash
# Build chart
bazel build //manman:manman_chart

# Install
helm install manman-v1 \
  bazel-bin/manman/host-services_chart/host-services \
  --namespace manman \
  --create-namespace

# Upgrade
helm upgrade manman-v1 \
  bazel-bin/manman/host-services_chart/host-services
```

## Documentation

- **[docs/README.md](./docs/README.md)** - Documentation index for features and deployment
- **[docs/PRODUCTION_DEPLOYMENT.md](./docs/PRODUCTION_DEPLOYMENT.md)** - Production setup and configuration
- **[docs/PARAMETER_SYSTEM.md](./docs/PARAMETER_SYSTEM.md)** - Parameter configuration system
- **[docs/BACKUP_SYSTEM.md](./docs/BACKUP_SYSTEM.md)** - Backup and restore system
- **[docs/THIRD_PARTY_IMAGES.md](./docs/THIRD_PARTY_IMAGES.md)** - Running custom Docker images

## Migration to V2

For new projects or deployments, use **[ManManV2](../manmanv2)** instead. It provides:

- **Go services** - Better performance and deployment model
- **Split-plane architecture** - Control plane + execution plane separation
- **Modern tooling** - gRPC, Protocol Buffers, improved developer experience
- **Better documentation** - Clear architecture and setup guides

See [../manmanv2/README.md](../manmanv2/README.md) to get started.

## Support

- **Questions about V1?** → Check [docs/README.md](./docs/README.md)
- **Need to migrate to V2?** → See [../manmanv2/GETTING_STARTED.md](../manmanv2/GETTING_STARTED.md)
- **Architecture questions?** → See [../manmanv2/ARCHITECTURE.md](../manmanv2/ARCHITECTURE.md)
