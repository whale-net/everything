# Tools — TOC

Build, release, and development tooling.

## Build & Release

- [helm/README.md](helm/README.md) — Bazel Helm chart generation system (quick start + common patterns)
- [helm/APP_TYPES.md](helm/APP_TYPES.md) — App type reference (`external-api`, `internal-api`, `worker`, `job`)
- `//tools:release` — Release automation CLI; see [`docs/RELEASE.md`](../docs/RELEASE.md) for usage
- [appmeta/README.md](appmeta/README.md) — Proto schema of record for `release_app` manifest JSON, shared by `release_helper_go`, the helm composer, and the app registry — consolidated in AR-M and in active use by all three
- [compose-resolver/README.md](compose-resolver/README.md) — App-agnostic sidecar for single-host `docker-compose` deployments: polls App Registry and redeploys one compose service to the promoted version automatically; see `manmanv2/host/RESOLVER.md` for a worked example
- [app_registry/TOC.md](app_registry/TOC.md) — App Registry: gRPC service indexing published artifacts and tracking per-environment promotion state. Recording is live in `release.yml` behind `APP_REGISTRY_CICD_OPT_IN` (off by default); version allocation is wired for domains at adoption stage `allocate` (issue #829/AR-5b — see `app_registry/PLAN.md`'s "AR-5" section); promotion and writeback are fully built but have never run for real — see [`app_registry/OPERATIONS.md`](app_registry/OPERATIONS.md) for the day-2 runbook

## Local Development

- [wireframe/README.md](wireframe/README.md) — UI wireframe kit: assembles daisyUI screen fragments into a clickable preview.html for design iteration
- [tilt/README.md](tilt/README.md) — Tilt configuration for local Kubernetes development
- [tilt-mcp/README.md](tilt-mcp/README.md) — Tilt MCP integration for AI-assisted development
- [tilt-mcp/CURSOR_INSTALL.md](tilt-mcp/CURSOR_INSTALL.md) — Cursor IDE integration setup
- [serial-mcp/README.md](serial-mcp/README.md) — ESP32 serial monitor MCP server (serial_tail, serial_grep, serial_status)
- [agentsync-mcp/README.md](agentsync-mcp/README.md) — cross-agent-session rendezvous MCP server: one session starts/joins a session with another and blocks on `sync()` until the peer replies
- [project-manager/README.md](project-manager/README.md) — AGY & Claude Code plugin: multi-persona GitHub-tracked planning pipeline (producer/architect/stakeholder/planner/worker/validator/system-validator)
- [project-manager/CONVENTIONS.md](project-manager/CONVENTIONS.md) — the plugin's GitHub label/workflow contract (issue kinds, lifecycle, worker unblock procedure) — every persona file follows this exactly
- [app_registry/TOC.md](app_registry/TOC.md) — includes the `app-registry` AGY/Claude Code plugin: three crystaldba `postgres-mcp` servers (`app-registry-pg-{tilt,dev,prod}`)

## Code Generation

- [client_codegen/README.md](client_codegen/README.md) — test that exercises a generated OpenAPI client; codegen targets themselves live under `//generated/...`
- [openapi/README.md](openapi/README.md) — OpenAPI tooling

## Platform-Specific

- [steamcmd/README.md](steamcmd/README.md) — SteamCMD packaging tool
- [lib32/README.md](lib32/README.md) — 32-bit library support

## Firmware / Embedded

- [firmware/README.md](firmware/README.md) — Hermetic ESP32 toolchain, esp32_firmware() macro, boards, flashing, Pigweed integration
- [firmware/esp32/arduino_core.BUILD](firmware/esp32/arduino_core.BUILD) — Arduino ESP32 core library targets (Wire, WiFi, etc.)
