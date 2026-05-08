# LeafLab — TOC

Plant and environment monitoring firmware and data pipeline.

## Local Development

- [Tiltfile](Tiltfile) — Start RabbitMQ with MQTT plugin for local sensor testing (`cd leaflab && tilt up`)

## Start Here

- [README.md](README.md) — What LeafLab is, quick start commands, relationship to `//firmware`
- [ARCHITECTURE.md](ARCHITECTURE.md) — Link-seam board config pattern, dynamic sensor factory, pipeline overview
- [DATA.md](DATA.md) — ER diagram, sensor identity model, config push flow, reading write path, mux_path format

## Projects

- [sensorboard/README.md](sensorboard/README.md) — Build, flash, extend the sensorboard firmware; how to add sensors and board configs

## Related Docs

- [firmware/README.md](../firmware/README.md) — ISensor, SensorReading, II2CBus, MQTTWriter, test doubles, adding sensors
- [tools/firmware/README.md](../tools/firmware/README.md) — Bazel toolchain, esp32_firmware() macro, flashing, WSL2 USB setup
