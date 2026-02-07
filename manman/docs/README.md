# ManMan Documentation Index

This directory contains feature documentation, guides, and reports for ManMan V2.

## Quick Navigation

### For Developers
- **[Architecture Overview](../ARCHITECTURE.md)** - System design, split-plane model, component overview
- **[Local Development Setup](../../manman-v2/README.md)** - Run ManManV2 locally with Tilt
- **[Quick Start Guide](../../manman-v2/QUICK-START.md)** - 5-minute getting started

### For Operators & Deployment
- **[Production Deployment Guide](./PRODUCTION_DEPLOYMENT.md)** - Secrets, configuration, security best practices
- **[Host Manager Deployment](../host/DEPLOYMENT.md)** - Bare metal host setup for game servers

### Feature Documentation
- **[Parameter System](./PARAMETER_SYSTEM.md)** - Configure game servers with parameters
- **[Backup & Restore System](./BACKUP_SYSTEM.md)** - Game save management and S3 integration
- **[Third-Party Images](./THIRD_PARTY_IMAGES.md)** - Running any Docker image as a game server
- **[Event Processing](./PHASE_6_COMPLETE.md)** - External event subscribers and monitoring

### Project Status & History
- **[Phase 6 Status](./PHASE_6_STATUS.md)** - Event processor roadmap and implementation plan
- **[Phase 6 Complete](./PHASE_6_COMPLETE.md)** - Event processor completion report

### Design Documents
- **[Design Directory](../design/)** - Detailed design documents and architectural decision records

---

## Documentation Organization

```
manman/
├── ARCHITECTURE.md              # V2 system architecture
├── README.md                    # Project overview (V1 + V2)
├── docs/                        # This directory - feature docs and guides
│   ├── README.md               # You are here
│   ├── PRODUCTION_DEPLOYMENT.md # Deployment guide (secrets, config, security)
│   ├── PARAMETER_SYSTEM.md     # Feature: Parameters
│   ├── BACKUP_SYSTEM.md        # Feature: Backups
│   ├── THIRD_PARTY_IMAGES.md   # Feature: Custom images
│   ├── PHASE_6_COMPLETE.md     # Report: Event processing
│   └── PHASE_6_STATUS.md       # Roadmap: Event processing
├── design/                      # Design documents
├── api/                         # V2 gRPC API service
├── processor/                   # V2 event processor service
├── host/                        # V2 host manager (bare metal)
│   └── DEPLOYMENT.md           # Host deployment guide
├── migrate/                     # Database migrations
└── src/                         # V1 services (legacy Python)

manman-v2/                       # Local development environment
├── README.md                    # Development setup with Tilt
├── QUICK-START.md              # 5-minute guide
├── ABOUT.md                    # Design decisions & Tiltfile overview
├── Tiltfile                    # Development orchestration
└── .env.example                # Configuration template
```

---

## Getting Started

### I want to...

#### Run ManManV2 locally for development
→ Go to [manman-v2/README.md](../../manman-v2/README.md)

#### Deploy ManManV2 to production
→ Read [PRODUCTION_DEPLOYMENT.md](./PRODUCTION_DEPLOYMENT.md) and [Architecture](../ARCHITECTURE.md)

#### Deploy the host manager on bare metal
→ Follow [host/DEPLOYMENT.md](../host/DEPLOYMENT.md)

#### Understand the V2 architecture
→ Read [ARCHITECTURE.md](../ARCHITECTURE.md)

#### Configure game servers with parameters
→ See [PARAMETER_SYSTEM.md](./PARAMETER_SYSTEM.md)

#### Set up backups for game saves
→ See [BACKUP_SYSTEM.md](./BACKUP_SYSTEM.md)

#### Run custom Docker images as game servers
→ See [THIRD_PARTY_IMAGES.md](./THIRD_PARTY_IMAGES.md)

#### Integrate with external monitoring/events
→ See [PHASE_6_COMPLETE.md](./PHASE_6_COMPLETE.md)

---

## Related Documentation

- **[API Proto Definitions](../protos/api.proto)** - gRPC service definitions
- **[Helm Charts](../BUILD.bazel)** - Kubernetes deployment via Helm
- **[Design Documents](../design/)** - Architectural decision records

---

Last Updated: 2026-02-07
