# ManMan V1 Documentation

**Note:** V1 is in maintenance mode. For new development, see [ManManV2](../../manmanv2).

This directory contains feature documentation and deployment guides for ManMan V1 services.

## Documentation Structure

```
docs/
├── README.md                    ← You are here
├── PRODUCTION_DEPLOYMENT.md     ← Production deployment and configuration
├── PARAMETER_SYSTEM.md          ← Game server parameter configuration
├── BACKUP_SYSTEM.md             ← Game save backup and restore
└── THIRD_PARTY_IMAGES.md        ← Running custom Docker images
```

## Quick Navigation

### For Operators
- **[PRODUCTION_DEPLOYMENT.md](./PRODUCTION_DEPLOYMENT.md)** - Deploy V1 to production
  - Kubernetes configuration
  - Environment variables and secrets
  - Security best practices

### Feature Documentation
- **[PARAMETER_SYSTEM.md](./PARAMETER_SYSTEM.md)** - Configure game servers
  - Parameter types and validation
  - Template rendering
  - Configuration merging

- **[BACKUP_SYSTEM.md](./BACKUP_SYSTEM.md)** - Backup game saves
  - S3 integration
  - Backup scheduling
  - Restore workflows

- **[THIRD_PARTY_IMAGES.md](./THIRD_PARTY_IMAGES.md)** - Use custom Docker images
  - Port mapping
  - Volume mounting
  - Image requirements

## Architecture

For architecture questions, see [../README.md](../README.md) or the V2 documentation:
- [../../manmanv2/ARCHITECTURE.md](../../manmanv2/ARCHITECTURE.md)

## Development

To work on V1 services locally:

```bash
# Build V1 services
bazel build //manman/...

# Run individual services
bazel run //manman/src/host:experience_api
bazel run //manman/src/host:status_api
bazel run //manman/src/host:worker_dal_api
```

See [../README.md](../README.md) for more details.

## Migrating to V2

V2 provides significant improvements:
- Go services (better performance)
- Split-plane architecture
- Modern tooling (gRPC, Protocol Buffers)
- Better documentation

Start here: [../../manmanv2/README.md](../../manmanv2/README.md)
