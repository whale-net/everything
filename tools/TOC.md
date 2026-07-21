# Tools — TOC

Build, release, and development tooling.

## Build & Release

- [helm/README.md](helm/README.md) — Bazel Helm chart generation system (quick start + common patterns)
- [helm/APP_TYPES.md](helm/APP_TYPES.md) — App type reference (`external-api`, `internal-api`, `worker`, `job`)
- `//tools:release` — Release automation CLI; see [`docs/RELEASE.md`](../docs/RELEASE.md) for usage

## Local Development

- [wireframe/README.md](wireframe/README.md) — UI wireframe kit: assembles daisyUI screen fragments into a clickable preview.html for design iteration
- [tilt/README.md](tilt/README.md) — Tilt configuration for local Kubernetes development
- [tilt-mcp/README.md](tilt-mcp/README.md) — Tilt MCP integration for AI-assisted development
- [tilt-mcp/CURSOR_INSTALL.md](tilt-mcp/CURSOR_INSTALL.md) — Cursor IDE integration setup
- [serial-mcp/README.md](serial-mcp/README.md) — ESP32 serial monitor MCP server (serial_tail, serial_grep, serial_status)
- [project-manager/README.md](project-manager/README.md) — Claude Code plugin: multi-persona GitHub-tracked planning pipeline (producer/architect/planner/writer/tester/validator/system-validator)
- [project-manager/CONVENTIONS.md](project-manager/CONVENTIONS.md) — the plugin's GitHub label/workflow contract (issue kinds, lifecycle, worker unblock procedure) — every persona file follows this exactly

## Code Generation

- [client_codegen/README.md](client_codegen/README.md) — OpenAPI client code generation
- [openapi/README.md](openapi/README.md) — OpenAPI tooling

## Platform-Specific

- [steamcmd/README.md](steamcmd/README.md) — SteamCMD packaging tool
- [lib32/README.md](lib32/README.md) — 32-bit library support

## Firmware / Embedded

- [firmware/README.md](firmware/README.md) — Hermetic ESP32 toolchain, esp32_firmware() macro, boards, flashing, Pigweed integration
- [firmware/esp32/arduino_core.BUILD](firmware/esp32/arduino_core.BUILD) — Arduino ESP32 core library targets (Wire, WiFi, etc.)
