# Getting Started with ManMan

Welcome! This guide helps you choose the right documentation based on what you want to do.

## Choose Your Path

### 🚀 I want to run ManManV2 locally for development

**Goal:** Set up a complete local development environment with all services running

**Start here:** [manman-v2/README.md](../manman-v2/README.md)

**What you'll get:**
- PostgreSQL, RabbitMQ, and all ManManV2 services running in Kubernetes via Tilt
- Hot reloading when you change code
- Test game server image for integration testing
- Port forwards to access services locally

**Time:** 10-15 minutes

---

### 📦 I want to deploy ManManV2 to production

**Goal:** Deploy ManManV2 control plane services to Kubernetes/cloud

**Start here:**
1. [ARCHITECTURE.md](./ARCHITECTURE.md) - Understand the system design
2. [docs/PRODUCTION_DEPLOYMENT.md](./docs/PRODUCTION_DEPLOYMENT.md) - Configure secrets and deploy

**What you'll need:**
- Kubernetes cluster
- PostgreSQL database
- RabbitMQ instance
- S3-compatible storage (optional, for backups)
- Bare metal servers with Docker (for host managers)

**Time:** 1-2 hours (depending on infrastructure)

---

### 🖥️ I want to run the host manager on bare metal

**Goal:** Set up a host manager to run game server containers

**Start here:** [host/DEPLOYMENT.md](./host/DEPLOYMENT.md)

**What you'll need:**
- Linux server with Docker installed
- Connection to ManManV2 control plane (RabbitMQ)
- Docker socket access

**Time:** 30 minutes

---

### 🏗️ I want to understand the V2 architecture

**Goal:** Learn how ManManV2 works and how components interact

**Start here:** [ARCHITECTURE.md](./ARCHITECTURE.md)

**What you'll learn:**
- Split-plane architecture (control plane vs execution plane)
- How the API, processor, and host manager work together
- RabbitMQ event flow
- Session lifecycle management

**Time:** 20-30 minutes reading

---

### ⚙️ I want to configure game servers with parameters

**Goal:** Use the parameter system to customize game server configurations

**Start here:** [docs/PARAMETER_SYSTEM.md](./docs/PARAMETER_SYSTEM.md)

**What you'll learn:**
- Parameter types and validation
- Parameter merging priority
- Template rendering
- Creating game configs with parameters

**Time:** 15 minutes

---

### 💾 I want to set up backups for game saves

**Goal:** Configure automatic backups to S3-compatible storage

**Start here:** [docs/BACKUP_SYSTEM.md](./docs/BACKUP_SYSTEM.md)

**What you'll learn:**
- Backup API usage
- S3 integration
- Restore workflows
- Backup scheduling

**Time:** 15 minutes

---

### 🐳 I want to run custom Docker images as game servers

**Goal:** Use any Docker image as a game server (not just preconfigured ones)

**Start here:** [docs/THIRD_PARTY_IMAGES.md](./docs/THIRD_PARTY_IMAGES.md)

**What you'll learn:**
- Third-party image support
- Configuration requirements
- Port mapping
- File mounting

**Time:** 10 minutes

---

### 📡 I want to integrate with external systems (monitoring, events)

**Goal:** Subscribe to ManManV2 events for monitoring or automation

**Start here:** [docs/PHASE_6_COMPLETE.md](./docs/PHASE_6_COMPLETE.md)

**What you'll learn:**
- External event subscriber pattern
- RabbitMQ integration
- Event types and payloads
- Example implementations

**Time:** 20 minutes

---

## Quick Reference

### Documentation Structure

```
manman/
├── GETTING_STARTED.md          ← You are here
├── ARCHITECTURE.md              ← System design & architecture
├── README.md                    ← Project overview
├── docs/                        ← Feature docs & guides
│   ├── README.md               ← Documentation index
│   ├── PRODUCTION_DEPLOYMENT.md ← Production deployment guide
│   ├── PARAMETER_SYSTEM.md     ← Parameter configuration
│   ├── BACKUP_SYSTEM.md        ← Backup & restore
│   └── ...
├── host/DEPLOYMENT.md          ← Host manager deployment
└── design/                      ← Design documents & ADRs

manman-v2/
├── README.md                    ← Local development setup
├── QUICK-START.md              ← 5-minute getting started
└── Tiltfile                    ← Development orchestration
```

### Common Commands

```bash
# Local development
cd manman-v2
tilt up

# Build Helm charts
bazel build //manman:manmanv2_chart

# Run tests
bazel test //manman/...

# Build host manager binary
bazel build //manman/host:host
```

### Getting Help

- **Questions about architecture?** → Read [ARCHITECTURE.md](./ARCHITECTURE.md)
- **Need deployment help?** → See [docs/PRODUCTION_DEPLOYMENT.md](./docs/PRODUCTION_DEPLOYMENT.md)
- **Local dev issues?** → Check [manman-v2/README.md](../manman-v2/README.md) troubleshooting section
- **Feature questions?** → Browse [docs/](./docs/)

---

## Next Steps

1. Choose your path above
2. Follow the linked documentation
3. Refer back to [docs/README.md](./docs/README.md) for additional guides

Happy coding! 🎮
