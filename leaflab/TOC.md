# LeafLab — TOC

Plant and environment monitoring firmware and data pipeline.

## Local Development

- [Tiltfile](Tiltfile) — Start RabbitMQ with MQTT plugin for local sensor testing (`cd leaflab && tilt up`)

## Start Here

- [README.md](README.md) — What LeafLab is, quick start commands, relationship to `//firmware`
- [ARCHITECTURE.md](ARCHITECTURE.md) — Link-seam board config pattern, dynamic sensor factory, pipeline overview, ownership/authorization boundary (admin standing lane vs. elevation, grant model, NFR18.1)
- [DATA.md](DATA.md) — ER diagram, sensor identity model, ownership model and closure (household root, A1, A23 staleness), SCD2 convention (incl. what is not SCD2 and why), config push flow, reading write path, mux_path format, analytical views reference

## Projects

- [sensorboard/README.md](sensorboard/README.md) — Build, flash, extend the sensorboard firmware; how to add sensors and board configs
- [sensorboard/CLAUDE.md](sensorboard/CLAUDE.md) — Agent instructions: flash/validate workflow, I2C hardware notes, config push, DB validation

## Related Docs

- [firmware/README.md](../firmware/README.md) — ISensor, SensorReading, II2CBus, MQTTWriter, test doubles, adding sensors
- [tools/firmware/README.md](../tools/firmware/README.md) — Bazel toolchain, esp32_firmware() macro, flashing, WSL2 USB setup
