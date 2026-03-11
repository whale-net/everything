# Everything Monorepo — Agent Instructions

## Behavioral Directives

- When refactoring libraries, search for usages and patches across the entire repo first.
- Provide short, straightforward responses. Elaborate only when necessary.
- Do not apologize for mistakes or praise the developer.
- If given a GitHub link for debugging, use GitHub MCP tools when available.
- Do not patch production environments — rely on release actions and human inputs.
- Read relevant docs before falling back to search or bash exploration.

## ⚠️ Critical: Cross-Compilation

Before touching image builds, platform targets, or container tooling: read [`docs/DOCKER.md`](docs/DOCKER.md).

This repo uses true cross-compilation for ARM64. Breakage is **silent at build time** and only fails at runtime. If `image-integration` tests fail, **do not merge**.

## Documentation Conventions

Each domain follows a standard file set. Use these as your primary reference before searching.

| File | Purpose | When to read it |
|------|---------|-----------------|
| `README.md` | Setup, local dev, and general usage | Starting work in a domain |
| `ARCHITECTURE.md` | System design, component relationships, data flow | Before making structural or cross-cutting changes |
| `ENV.md` | All environment variables for the domain or component | Configuring, deploying, or debugging runtime behavior |
| `TOC.md` | Index of concepts pointing to deeper docs | Finding domain-specific docs on a topic |

Not every domain has all four files — `ENV.md` is only present where runtime configuration applies, `ARCHITECTURE.md` may be omitted for simple utilities.

## Domains

| Domain | Description | Reference |
|--------|-------------|-----------|
| `manmanv2/` | Active game server orchestration platform (Go + Python) | [TOC](manmanv2/TOC.md) |
| `manman/` | Legacy V1 system — maintenance mode only | [TOC](manman/TOC.md) |
| `libs/` | Shared Python and Go libraries | [TOC](libs/TOC.md) |
| `tools/` | Build, release, Helm, and development tooling | [TOC](tools/TOC.md) |
| `friendly_computing_machine/` | Slack bot with Temporal workflows | [TOC](friendly_computing_machine/TOC.md) |
| `docs/` | Cross-cutting infrastructure and build system docs | [TOC](docs/TOC.md) |
| `demo/` | Example applications — see individual READMEs | — |
| `generated/` | Auto-generated OpenAPI clients — do not edit manually | — |
