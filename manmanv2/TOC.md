# ManManV2 — TOC

Active game server orchestration platform. Split-plane architecture: cloud control plane + bare-metal host managers.

## Start Here

- [README.md](README.md) — Local development setup, quick start, and workflow
- [ABOUT.md](ABOUT.md) — What ManManV2 is and why it exists
- [ARCHITECTURE.md](ARCHITECTURE.md) — Split-plane design, component relationships, data flow

## Components

- [ui/README.md](ui/README.md) — UI overview and handler patterns
- [ui/DESIGN_SYSTEM.md](ui/DESIGN_SYSTEM.md) — HTMX + Go template design system and component library
- [ui/HANDLER_MIGRATION.md](ui/HANDLER_MIGRATION.md) — Migrating handlers to the current pattern
- [processor/README.md](processor/README.md) — Event processor overview
- [processor/VERIFICATION.md](processor/VERIFICATION.md) — Verifying processor behavior
- [log-processor/README.md](log-processor/README.md) — Log processing pipeline
- [host/DEPLOYMENT.md](host/DEPLOYMENT.md) — Bare metal host manager deployment

## Configuration

- [ui/ENV.md](ui/ENV.md) — UI service environment variables
- [api/S3_CONFIG.md](api/S3_CONFIG.md) — S3/object storage configuration

## Testing

- [testdata/README.md](testdata/README.md) — Test fixtures and test data patterns
- [testdata/BAZEL_LIMITATION.md](testdata/BAZEL_LIMITATION.md) — Known Bazel limitation with testdata

## Archive

Self-registration feature docs in [docs/ARCHIVE/](docs/ARCHIVE/) — feature complete, archived for reference.
