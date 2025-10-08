# Documentation Archive

This directory contains historical and detailed documentation that supplements the main repository documentation.

## Purpose

The main repository focuses on essential documentation that developers need for day-to-day work:
- `README.md` - Project overview
- `AGENT.md` - Comprehensive agent instructions
- `docs/HELM_RELEASE.md` - Helm release system

This archive preserves detailed guides, implementation plans, and historical migration documents for:
- Deep-dive technical references
- Historical context and decision rationale
- Migration guides and lessons learned

## Directory Structure

### `detailed-guides/`
In-depth guides covering specific topics:
- `BUILDING_CONTAINERS.md` - Multi-platform container building deep dive
- `HELM_RELEASE_INTEGRATION.md` - Release system and Helm integration details
- `HELM_REPOSITORY.md` - GitHub Pages Helm repository management

### `helm-implementation/`
Helm chart system implementation documentation:
- `IMPLEMENTATION_PLAN.md` - Original architecture and planning document
- `MIGRATION.md` - Migration guide from manual charts to generated system
- `K8S_MANIFESTS.md` - Kubernetes manifest generation technical details
- `TEMPLATES.md` - Template system implementation reference

### `migration-2025-10/`
Historical record of the October 2025 multiplatform simplification:
- Migration from custom wrappers to standard Bazel rules
- Simplification of the build system
- Cross-compilation improvements
- Test fixes and validation

## When to Use This Archive

**Use main docs when:**
- Getting started with the project
- Working on day-to-day development
- Looking for quick reference

**Use archive docs when:**
- Need deep technical details
- Understanding architectural decisions
- Troubleshooting complex issues
- Contributing major changes

## Maintenance

Archive documents are preserved as-is for historical reference. They may not reflect the current state of the codebase but remain valuable for understanding the evolution and design rationale of the system.
