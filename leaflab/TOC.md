# LeafLab — TOC

Plant and environment monitoring firmware and data pipeline.

## Local Development

- [Tiltfile](Tiltfile) — Start RabbitMQ with MQTT plugin for local sensor testing (`cd leaflab && tilt up`)

## Start Here

- [README.md](README.md) — What LeafLab is, quick start commands, relationship to `//firmware`
- [PRODUCT.md](PRODUCT.md) — Product brief: vision, personas, load-bearing decisions, and a jump table to the current state, capability map, and milestone roadmap. Read before scoping or designing any leaflab milestone.
- [ARCHITECTURE.md](ARCHITECTURE.md) — Link-seam board config pattern, dynamic sensor factory, pipeline overview
- [DATA.md](DATA.md) — ER diagram, sensor identity model, SCD2 convention, config push flow, reading write path, mux_path format, analytical views reference
- [ENV.md](ENV.md) — Domain-level environment variables; currently just the `leaflab-data` Postgres MCP plugin's tilt/dev/prod connection vars

## Projects

- [sensorboard/README.md](sensorboard/README.md) — Build, flash, extend the sensorboard firmware; how to add sensors and board configs
- [sensorboard/CLAUDE.md](sensorboard/CLAUDE.md) — Agent instructions: flash/validate workflow, I2C hardware notes, config push, DB validation
- [ui/README.md](ui/README.md) — `leaflab-ui`: HTMX/templ web app, OIDC sign-in, DB-backed sessions, `leaflab_user` resolution, gRPC-only data access
- [ui/ENV.md](ui/ENV.md) — `leaflab-ui` environment variables (auth, session, OIDC client, `leaflab-api` address)

## Related Docs

- [firmware/README.md](../firmware/README.md) — ISensor, SensorReading, II2CBus, MQTTWriter, test doubles, adding sensors
- [tools/firmware/README.md](../tools/firmware/README.md) — Bazel toolchain, esp32_firmware() macro, flashing, WSL2 USB setup
