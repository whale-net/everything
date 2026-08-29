# LeafLab — TOC

Plant and environment monitoring firmware and data pipeline.

## Local Development

- [Tiltfile](Tiltfile) — Start RabbitMQ with MQTT plugin for local sensor testing (`cd leaflab && tilt up`)

## Start Here

- [README.md](README.md) — What LeafLab is, quick start commands, relationship to `//firmware`
- [ARCHITECTURE.md](ARCHITECTURE.md) — Link-seam board config pattern, dynamic sensor factory, pipeline overview, cross-process cache invalidation (FR73/NFR15), the bounded read path over tiers, two-phase boundary capture (FR20)
- [DATA.md](DATA.md) — ER diagram (incl. plant/plant_region_history/boundary_capture/boundary_partial/tier continuous aggregates), sensor identity model, SCD2 convention, config push flow, reading write path, mux_path format, analytical views reference and FR72 attribution correction, granularity-tier retention and the pre-aggregated-is-not-de-identified note, FR26.3 suspect checks and the stale-attribution window, canonical hardware key (FR18)

## Projects

- [sensorboard/README.md](sensorboard/README.md) — Build, flash, extend the sensorboard firmware; how to add sensors and board configs
- [sensorboard/CLAUDE.md](sensorboard/CLAUDE.md) — Agent instructions: flash/validate workflow, I2C hardware notes, config push, DB validation

## Related Docs

- [firmware/README.md](../firmware/README.md) — ISensor, SensorReading, II2CBus, MQTTWriter, test doubles, adding sensors
- [tools/firmware/README.md](../tools/firmware/README.md) — Bazel toolchain, esp32_firmware() macro, flashing, WSL2 USB setup
